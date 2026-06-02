#!/usr/bin/env bash
set -euo pipefail

echo "=== Step 1: Setting up Local Infrastructure ==="
docker run -d -p 5000:5000 --name local-oci-registry registry:2

echo "=== Step 2: Creating and Publishing OCI Firmware Artifact ==="
echo "EMBEDDED-FIRMWARE-BINARY-PAYLOAD-DATA-010101" > dummy_firmware.bin
oras push localhost:5000/firmware/test-bmc:1.0.0 --artifact-type application/vnd.openchami.firmware.bundle.v1+json dummy_firmware.bin:application/vnd.openchami.firmware.payload.v1

echo "=== Step 3: Launching JIT Firmware Execution Service ==="
# 1. Build the binary
GOTOOLCHAIN=go1.26.3 go build -o ./tmp/server ./cmd/server

# 2. Run the binary in the background
./tmp/server serve --port 8090 --database-url="file:readme_test.db?cache=shared&_fk=1" &

# 3. Capture the actual PID
SERVER_PID=$!
sleep 3

echo "=== Step 4: Submitting JIT Firmware Update Job ==="
JOB_RESPONSE=$(curl -sS -X POST http://127.0.0.1:8090/firmwareupdatejobs/ -H 'Content-Type: application/json' -d '{"metadata":{"name":"readme-jit-job"},"spec":{"targetAddress":"192.0.2.200","username":"admin","password":"password","ociReference":"localhost:5000/firmware/test-bmc:1.0.0","targets":["/redfish/v1/UpdateService/FirmwareInventory/BMC"],"serverProxyAddress":"127.0.0.1"}}')
echo "Server Response: $JOB_RESPONSE"

# Extract the system UID dynamically from the raw JSON string
JOB_UID=$(echo "$JOB_RESPONSE" | grep -oE '"uid":"[^"]+"' | head -n 1 | sed 's/"uid":"//' | sed 's/"//')
echo "Extracted Job UID: $JOB_UID"

echo "=== Step 5: Monitoring Asynchronous Reconciliation State ==="
# Poll the status endpoint until the state machine transitions out of setup phases
JOB_STATUS=""
for i in {1..20}; do
  JOB_STATUS=$(curl -sS http://127.0.0.1:8090/firmwareupdatejobs/$JOB_UID/)
  CURRENT_STATE=$(echo "$JOB_STATUS" | grep -oE '"jobState":"[^"]+"' | head -n 1 | sed 's/"jobState":"//' | sed 's/"//' || echo "Pending")
  echo "Polled State: $CURRENT_STATE"
  if [ "$CURRENT_STATE" != "Pending" ] && [ "$CURRENT_STATE" != "Resolving" ]; then
    break
  fi
  sleep 5
done

echo "=== Step 6: Validating the Just-In-Time Proxy Endpoint ==="
# Extract the actual resolved payload digest computed by the reconciler logic
TARGET_DIGEST=$(echo "$JOB_STATUS" | grep -oE 'sha256:[a-f0-9]{64}' | head -n 1 || echo "")

if [ -z "$TARGET_DIGEST" ]; then echo "Error: Could not extract resolved digest from job status. Reconciler might still be executing."; else curl -i http://127.0.0.1:8090/firmware-proxy/layer/$TARGET_DIGEST; fi

echo "=== Step 7: Tearing Down Infrastructure ==="
kill $SERVER_PID
docker rm -f local-oci-registry
rm -rf ./tmp
rm dummy_firmware.bin readme_test.db
echo "=== Workflow Verification Complete ==="
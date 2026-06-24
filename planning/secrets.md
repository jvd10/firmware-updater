## 1. Context Acquisition

The goal is to eliminate raw credential storage within the `FirmwareUpdateJob` resource. The `FirmwareUpdateJobSpec` currently stores `Username` and `Password` in plaintext. This will be replaced with a `SecretID` field. The reconciler will dynamically resolve this ID into credentials at runtime using the `github.com/OpenCHAMI/magellan/pkg/secrets` package, which decrypts a local `secrets.json` file using a `MASTER_KEY` environment variable. The `secrets.json` file itself will be provisioned out-of-band by operators using the standard Magellan CLI.

## 2. Code Changes

* **Dependency Management:**
* Run `go get github.com/OpenCHAMI/magellan` to add the required dependency to `go.mod`.


* **Resource Schema Updates (`apis/hardware.fabrica.dev/v1/firmwareupdatejob_types.go`):**
* In `FirmwareUpdateJobSpec`, remove the `Username string` and `Password string` fields.
* Add `SecretID string `json:"secretID" validate:"required"`` to `FirmwareUpdateJobSpec`.


* **Reconciler Logic Updates (`pkg/reconcilers/firmwareupdatejob_reconciler.go`):**
* **Imports:** Add `"os"` and `"github.com/OpenCHAMI/magellan/pkg/secrets"`.
* **Pre-flight Check:** At the top of `reconcileFirmwareUpdateJob`, verify the `MASTER_KEY` environment variable. If `os.Getenv("MASTER_KEY") == ""`, return a terminal error to halt reconciliation and update the status to `Failed`, as this is a server-level misconfiguration.
* **Credential Retrieval:** Before calling discovery or dispatch functions, insert the following logic:
1. Determine the secret store path (e.g., default to `"secrets.json"`, or allow an environment variable override like `SECRETS_FILE_PATH`).
2. Invoke `secrets.OpenStore(path)`.
3. Call `GetSecretByID(res.Spec.SecretID)`. If the secret is not found, treat this as a terminal error (update status to `Failed` and return).
4. Unmarshal the returned JSON string into a temporary map or struct. Extract the `username` and `password` strings. If parsing fails or keys are missing, return a terminal error.


* **Function Signature Updates:** Modify `discoverUpdateServiceActionWithBackoff`, `discoverUpdateServiceAction`, `discoverTargetsFromInventoryWithBackoff`, `discoverTargetsFromInventory`, `dispatchRedfishWithBackoff`, and `dispatchRedfishOnce` to accept the extracted `username` and `password` as standard string arguments instead of pulling them from `res.Spec.Username` and `res.Spec.Password`.



## 3. Acceptance Criteria

* **Compilation:** The code must compile successfully after running `go mod tidy` and `go build ./...`.
* **Testing:** Run `go test ./...`. Unit tests for the reconciler must be updated. You must mock the filesystem or the `SecretStore` interface to verify that terminal errors are properly generated when `MASTER_KEY` is missing or when a `SecretID` is invalid.
* **Error State Verification:** The reconciler must properly transition the `FirmwareUpdateJob` status to `Failed` with a descriptive `ErrorDetail` without infinitely retrying when encountering a missing secret or malformed payload.
* **Credential Injection:** The Redfish HTTP requests must successfully utilize the decrypted Basic Auth credentials to negotiate with the BMC.

## Output Artifacts

Upon meeting all Acceptance Criteria, generate a `HANDOFF-PHASE2.md` file in the planning directory containing:

1. A brief summary of the implemented logic.
2. The exact, verified `curl` command that successfully tested the code with the updated API spec.
3. Detailed notes on important details for using the code that was implemented, whereby someone with no context could fully utilize the code as expected and fully understand the implementation (specifically detailing the requirement to use the Magellan CLI to provision the `secrets.json` file prior to running the service).
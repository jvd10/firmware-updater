# Phase 3 E2E Validation Handoff

## 1. Successful GET Response JSON (Step 4.3)

```json
{"apiVersion":"v1","kind":"FirmwareBundle","metadata":{"name":"local-test-bundle-phase3","uid":"firmwarebundle-79e6c7c9","createdAt":"2026-06-01T13:00:36.532294-07:00","updatedAt":"2026-06-01T13:00:36.550452-07:00"},"spec":{"registryURL":"localhost:5000","repository":"firmware/test-bmc","tagOrDigest":"1.0.0"},"status":{"discovered":true,"manifestDigest":"sha256:3ef2f560a0da28f6c3db229556fae32afbd39b668605bfdc730f3486f020a561","extractedMetadata":{"org.openchami.firmware.component":"bmc","org.openchami.firmware.version":"1.0.0","org.opencontainers.image.created":"2026-06-01T19:58:57Z","payloadDigest":"sha256:b1205ada5511f72769aca484a3ec22c975a155a17184ad66f487acd8e3344ce5"}}}
```

## 2. ORAS Reconciler Authentication and Manifest Pull Flow

The FirmwareBundle reconciler in `pkg/reconcilers/firmwarebundle_reconciler.go` performs discovery by constructing an ORAS remote repository from:

- `registryURL`
- `repository`
- `tagOrDigest`

Execution sequence:

1. Build repository client with `remote.NewRepository("<registry>/<repository>")`.
2. For loopback endpoints (`localhost`, `127.0.0.1`, `[::1]`), set `repo.PlainHTTP = true` so the local insecure registry is contacted over HTTP.
3. Resolve tag/digest to a descriptor via `repo.Resolve(...)`.
4. Download manifest bytes via `content.FetchAll(...)`.
5. Unmarshal OCI manifest and validate artifact type is `application/vnd.openchami.firmware.bundle.v1+json`.
6. Copy manifest annotations and first layer digest into `status.extractedMetadata`.
7. Set `status.discovered = true` and write `status.manifestDigest`.

Authentication behavior:

- This implementation does not yet inject explicit credentials into the ORAS client.
- Registry access is anonymous by default.
- For registries that require auth, ORAS/registry calls will receive auth challenge responses and reconciliation will surface those errors in `status.error`.

## 3. Server Log Lines Showing ORAS Pull Sequence

```text
2026/06/01 13:00:36 "POST http://127.0.0.1:8080/firmwarebundles/ HTTP/1.1" from 127.0.0.1:55972 - 201 342B in 2.410375ms
[DEBUG] Processing reconciliation for FirmwareBundle/firmwarebundle-79e6c7c9 (reason: Event: firmware-updater.resource.firmwarebundle.created)
[DEBUG] Reconciling FirmwareBundle FirmwareBundle/firmwarebundle-79e6c7c9
[INFO] FirmwareBundle firmwarebundle-79e6c7c9 discovered from registry with manifest sha256:3ef2f560a0da28f6c3db229556fae32afbd39b668605bfdc730f3486f020a561
[DEBUG] Reconciliation successful for FirmwareBundle/firmwarebundle-79e6c7c9
2026/06/01 13:00:36 "GET http://127.0.0.1:8080/firmwarebundles/firmwarebundle-79e6c7c9/ HTTP/1.1" from 127.0.0.1:55974 - 200 684B in 398µs
```

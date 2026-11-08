# GPU utilization tint for Storm

The `custom/gpu-storm` branch adds optional GPU utilization input to the existing
Storm effect. Its base color gradually blends toward a configured busy color as
GPU usage increases. The effect's white flashes, brightness, and speed settings
continue to work as before.

This branch starts from upstream commit
`a08204cf88fd373a0bbdbe1f4382e8c64024adca` (0.9.4), the revision used for the
tested deployment. The custom build version is `0.9.4-gpu-storm1`.

## Configuration

Select Storm for the channels you want to tint, then choose their idle base color
in the existing RGB editor. For example, blue `(32, 62, 77)` blends toward purple
`(62, 32, 77)` with the configuration below. The setting affects every Storm
channel; other effects keep their existing behavior.

Find the stable UUIDs of the NVIDIA GPUs to include:

```sh
nvidia-smi --query-gpu=uuid,name --format=csv,noheader
```

Merge this key into the existing `config.json`, replacing the example UUIDs with
the GPUs you want to average. Preserve the other configuration keys.

```json
{
  "gpuStorm": {
    "enabled": true,
    "gpuUUIDs": [
      "GPU-replace-with-first-uuid",
      "GPU-replace-with-second-uuid",
      "GPU-replace-with-third-uuid",
      "GPU-replace-with-fourth-uuid"
    ],
    "busyColor": [62, 32, 77],
    "smoothingSeconds": 5,
    "sampleIntervalMilliseconds": 500
  }
}
```

Each selected GPU has equal weight. One GPU at 100% with three idle GPUs produces
25% aggregate utilization. At 0%, Storm uses its saved base color; at 100%, it
uses `busyColor`. Intermediate values blend the RGB components linearly.

The sampler runs independently of rendering. With the settings above, it queries
`nvidia-smi` every 500 ms, while each rendered frame uses a trailing, time-weighted
five-second average. A step from idle to full utilization therefore changes the
tint over five seconds after it is sampled. Startup also ramps from the idle tint.

All configured GPUs must return valid utilization. If sampling fails or a GPU is
missing, the effect holds its current tint until telemetry recovers. Each query
has a two-second timeout. The OpenLinkHub service account needs access to
`nvidia-smi` and the selected GPUs.

Omitting `gpuStorm` or setting `enabled` to `false` retains the original Storm
behavior. Invalid enabled configurations are logged and leave the feature off.
Configuration changes take effect on the next OpenLinkHub restart. Restarting the
service can briefly interrupt lighting during normal device initialization; load
changes during operation do not restart the effect or write saved profiles.

## Status

`GET /api/gpuStorm` returns the cached sampler state in the response's `data` field:

| Field | Meaning |
| --- | --- |
| `enabled` | Whether the sampler is enabled. |
| `rawPercent` | Most recent valid aggregate utilization, from 0 to 100. |
| `smoothedPercent` | Current five-second average with the example configuration. |
| `lastSuccess` | Timestamp of the latest valid sample; zero before the first sample. |
| `error` | Sampling error, present only while telemetry is unavailable. |

```sh
curl -s http://localhost:27003/api/gpuStorm
```

Use the address and port configured for your installation. Reading this endpoint
does not trigger a GPU query. The regular device API still reports the saved RGB
profile, because the utilization tint is applied to a copy while rendering.

## Build and test

Use the Go and native development dependencies listed in the main README. This
branch was tested with Go 1.26.10. Build a binary without installing it:

```sh
mkdir -p build
CGO_CFLAGS_ALLOW=-fno-strict-overflow go test -race ./src/gputint ./src/rgb
CGO_CFLAGS_ALLOW=-fno-strict-overflow go build -trimpath \
  -ldflags '-X OpenLinkHub/src/version.Version=0.9.4-gpu-storm1' \
  -o build/OpenLinkHub .
```

The renderer tests use a fake `nvidia-smi` visible only to their test process and
never open lighting devices. For an additional read-only hardware query, set
`OPENLINKHUB_TEST_GPU_UUIDS` to a comma-separated list of real GPU UUIDs before
running the tests.

Tests cover smoothing in both directions, irregular samples, missing GPUs,
recovery, configuration validation, concurrent readers, cancellation, and repeated
Storm transitions without black reset frames or profile mutation. Validation on
the original deployment passed all 12 tests, including the actual GPU query, and
confirmed 14 Storm channels across one Commander Core and two LINK controllers.

Before replacing an installed binary, back up that binary and `config.json`.
Install the custom binary while preserving ownership and permissions, merge the
configuration above, and restart the service once. A later upstream binary upgrade
replaces this customization unless it also includes the patch.

# OCI Compute availability

Standalone Go collector that periodically calls OCI's **CreateComputeCapacityReport**
API for configured shapes, shape sizes, and optional fault domains.
Subscribed regions and availability domains are discovered automatically unless
explicitly restricted.
Project/repository name: `oci-compute-availability`.
Run continuously in Kubernetes to give teams a self-service Grafana view of
where their desired shape and sizing combinations have recently been available.
It does not launch instances, reserve capacity, or scan other compartments.
Capacity reports use the tenancy root compartment.

## Run and build

Requires Go 1.26+, a C compiler for race tests, and Docker with Buildx for image
builds. No OCI access is needed to build or test.

Run `make` or `make help` to list all build targets, settings, and examples.

Run `./dist/collector` with no arguments to print the full help and exit
successfully. `--help` and `-h` also display the full help.

```bash
make build # runs race tests and go vet, then builds dist/collector
cp queries.example.json queries.json
# Edit queries.json to select your shapes/sizes; region and AD are optional.
./dist/collector --once --auth config \
  --profile DEFAULT --config-file "$HOME/.oci/config" \
  --queries queries.json
```

The local scan reads the tenancy OCID from your selected OCI profile and
discovers subscribed regions and AD names automatically. No tenancy OCID,
region, or AD name is required in the
default query sample. `--once` prints JSON and exits; it does not start the HTTP
service. A complete scan returns `"success":true` and exit code 0. A failed scan
returns errors and a nonzero exit code. Run `make test` for automated tests
without OCI access. See [Optional local credentials](#optional-local-credentials)
for the required API-key profile fields.

Automatic region/AD discovery and directory input require a build from the updated
source. The previously published `v0.1.0` image requires explicit regions and AD names.
Publish a new version before using these samples in a container.

## Query configuration

The samples are input templates, not collected data.
[queries.example.json](queries.example.json) contains 36 queries: 12 configurations
each for `VM.Standard.E5.Flex`, `VM.Standard.E6.Flex`, and
`VM.Standard.E6.Ax.Flex`, using the names in Oracle's
[Compute Shapes reference](https://docs.oracle.com/en-us/iaas/Content/Compute/References/computeshapes.htm).
Each shape uses these OCPU and memory combinations:

| OCPUs | Memory variants (GB) |
| --- | --- |
| 2 | 8, 16, 32 |
| 4 | 16, 32, 64 |
| 8 | 32, 64, 128 |
| 16 | 64, 128, 256 |

Each entry has its own query name. For example:

```json
[
  {
    "name": "e5-2cpu-16gb",
    "shape": "VM.Standard.E5.Flex",
    "shapeConfig": {"ocpus": 2, "memoryInGBs": 16}
  }
]
```

Each query requires a globally unique `name` and a `shape`.
Omit `region` to scan all of the authenticated tenancy's subscribed regions,
or set `"region": "us-ashburn-1"` to restrict that query to Ashburn.
Subscriptions are discovered once per scan using `ListRegionSubscriptions`;
the profile's region selects the initial API endpoint, not the scan scope.
Empty subscription lists and discovery failures fail the scan. Returned
subscriptions are not silently dropped based on readiness; an inaccessible
region causes the scan to fail.
Omit `availabilityDomain` to query every AD
discovered for your tenancy in that region, or set it to restrict the query to
one AD (an explicit AD requires an explicit region). AD discovery uses the
authenticated identity and tenancy, once per region
per scan. Results and metrics include the resolved AD names. Discovery failures
or an empty domain list mark the scan incomplete and expose unknown outcomes;
successful checks elsewhere remain visible.
The principal must be allowed to call `ListRegionSubscriptions` and
[ListAvailabilityDomains](https://docs.oracle.com/en-us/iaas/tools/go/latest/example/index.html#example-IdentityClient.ListAvailabilityDomains).
Flex shapes require explicit
`shapeConfig.ocpus` and `shapeConfig.memoryInGBs`. Optional `faultDomain` narrows
the request. Leave it absent for OCI's report across fault domains. Optional
shape configuration also supports `nvmes` and `baselineOcpuUtilization`.
Use distinct query names for different shape sizes. Results for overlapping
queries are alternative observations; do not add their available counts together.
Only shapes supported by the capacity-report API can be queried; the service's
API validation is authoritative. Unknown query-file fields are rejected.

The process scans immediately and waits five minutes **after completion** before
scanning again. `--interval` accepts Go durations (minimum 30s); `--timeout` bounds
a complete scan (default 10m). Up to six regions are scanned concurrently by
default (`--concurrency 1..32`); requests within each region remain sequential.
Regional SDK clients are reused and have bounded HTTP timeouts and retries.
A single replica avoids duplicate
requests. Query configuration is loaded at startup; restart after changing it.

## Runtime identity

| Runtime | Authentication argument |
| --- | --- |
| OKE pod | `--auth workload` (default) |
| Container on OCI Compute | `--auth instance` |
| OCI Container Instances | `--auth resource` |

Deployed service mode supports only these runtime identities. Configure workload
IAM for the OKE service account, or dynamic groups and IAM policies for Compute
and Container Instances. Compute containers must reach the host's metadata
service. Container Instances must expose their resource principal to the container.
For workload identity set `OCI_RESOURCE_PRINCIPAL_VERSION=2.2` and the initial
`OCI_RESOURCE_PRINCIPAL_REGION`. Request regions come from tenancy subscriptions,
unless restricted in the query file.
`OCI_TENANCY_OCID` is required for every runtime identity.

For example, inside an OKE pod with workload identity configured:

```bash
export OCI_TENANCY_OCID=ocid1.tenancy.oc1..REPLACE
./dist/collector --auth workload --queries queries.json --interval 5m
```

The environment variable supplies the tenancy for runtime identities; it is
not required with local `--auth config`. AD discovery applies to every auth mode.

Grant the chosen principal `COMPUTE_CAPACITY_REPORT_CREATE` on
`compute-capacity-reports` in the tenancy. For a dynamic group, the policy form is:

```text
Allow dynamic-group <your-dynamic-group> to manage compute-capacity-reports in tenancy
```

For OKE, use a workload-identity policy scoped to your exact cluster, namespace,
and service account. Do not put user credentials in the pod. See the official
[capacity-report IAM permissions](https://docs.oracle.com/en-us/iaas/Content/Identity/Reference/corepolicyreference.htm),
[OKE workload identity](https://docs.oracle.com/en-us/iaas/Content/ContEng/Tasks/contenggrantingworkloadaccesstoresources.htm),
and [OCI SDK authentication](https://docs.oracle.com/en-us/iaas/Content/API/Concepts/sdk_authentication_methods.htm).

## Optional local credentials

Local one-shot validation can explicitly load a standard OCI SDK INI properties
file. There is no default-file discovery, identity fallback, or Terraform-variable
authentication. File authentication is rejected for the long-running service.
The tenancy OCID comes from the selected profile; query JSON requires neither
a tenancy OCID, region, nor an availability-domain name.

Keep the following file and its API key **outside this repository**:

```ini
[DEFAULT]
tenancy=ocid1.tenancy.oc1..REPLACE
user=ocid1.user.oc1..REPLACE
fingerprint=REPLACE
key_file=/secure/path/api-key.pem
region=us-ashburn-1
```

```bash
./dist/collector --once --auth config \
  --profile DEFAULT --config-file "$HOME/.oci/config" \
  --queries queries.json
# Replace DEFAULT with another named profile if needed.
```

Restrict file/key permissions. Never commit, deploy, or bake them into an image.
Git ignores `*.properties`, `*.pem`, `*.key`, `.oci/`, and `oci-config*`.
The Docker build context allows only Go source, module files and Dockerfile.
Query JSON contains workload requirements, not credentials; mount it separately.

## Results and monitoring

HTTP listens on `:9109` (`--listen` overrides it):

- `/metrics`: Prometheus gauges with query, region, AD, shape, OCPU, memory and fault-domain labels.
- `/results`: latest scan, including successful results and structured failures with check times.
- `/healthz`: process liveness, independent of OCI collection success.

`--once` prints the snapshot as JSON and exits nonzero if incomplete. A partial
snapshot still includes its successful results. Each result has `checkedAt`;
`failures` contains query scope, `checkedAt`, failure stage, and error details.
Failed checks replace previous observations with unknown outcomes; old available
capacity is never silently reused after an error. Discovery failures use
`UNKNOWN` region/AD labels when those names could not be resolved.
Results are held in memory; Prometheus provides metric history. No database or
local result files are written by the service. Protect `/results` and `/metrics`
with your cluster network policy; these endpoints have no built-in HTTP authentication.

A failed region/AD discovery, failed query, malformed response, or unknown status
marks only the affected check unknown. Successful observations from that scan
remain available in JSON and metrics. Global diagnostics include
`oci_capacity_collection_success`, `oci_capacity_collection_errors`, duration,
and attempt/last-success timestamps. Global success is zero for partial scans;
the last-success timestamp means the most recent fully successful scan, rather
than the freshness of every individual result. HTTP liveness alone does not
establish successful collection.

`oci_capacity_state` provides per-observation states: 0 unknown, 1 available,
2 out of capacity, 3 unsupported. `oci_capacity_checked_timestamp_seconds`
records each check's completion time. The dashboard computes age from that time
and displays state 4 (stale) after the selected threshold. Failed checks emit
state 0 and `oci_capacity_check_failure{stage="..."}`; they emit neither available
counts nor binary availability. Error text stays in logs and JSON.

`oci_capacity_available_count` is emitted only when OCI supplies a count: missing
counts are unknown, not zero. `oci_capacity_available` is one only for `AVAILABLE`;
`oci_capacity_status` distinguishes `OUT_OF_HOST_CAPACITY` and
`HARDWARE_NOT_SUPPORTED`. Query names identify the full requested configuration.
Capacity is an observation at request time, not a reservation or launch guarantee.
The scan is not a simultaneous global snapshot.

## Retrieval speed and progress

The 36-query default matrix produces 36 capacity requests per AD in every
subscribed region. `--concurrency` bounds the number of active regional scans,
including AD discovery. Default: 6. Use 1 for sequential retrieval or increase
gradually while watching for throttling and network errors. Concurrency reduces
elapsed time across regions; it does not reduce the number of OCI requests.

```bash
./dist/collector --once --auth config \
  --profile DEFAULT --config-file "$HOME/.oci/config" \
  --queries queries.example.json --concurrency 6
```

Progress is written to stderr: scan scope, regional start/completion, result/error
counts, and regional elapsed time. One-shot result JSON remains on stdout.
`--timeout` still limits the entire scan, including discovery, retries, and queued
regions. Cancellation stops further capacity requests and marks uncompleted
checks unknown. Runtime failures in one region mark the full scan incomplete
while preserving successful observations from the other checks.

For a quick smoke test, use a smaller query file or add an explicit `region`
filter. This narrows coverage; `--concurrency` preserves the configured scope.
An EOF error indicates a failed response, rather than evidence of unavailable
capacity. The OCI SDK's existing retry policy remains enabled.

See [CreateComputeCapacityReport](https://docs.oracle.com/en-us/iaas/tools/oci-cli/latest/oci_cli_docs/cmdref/compute/compute-capacity-report/create.html).

## Kubernetes deployment

`deploy/kubernetes.yaml` supplies one Deployment, ServiceAccount, query ConfigMap,
and Service. Replace the tenancy OCID and image version;
configure workload IAM before applying into your chosen namespace. No credential
file is deployed. Its 36 sample queries discover all subscribed regions and ADs;
edit the shapes/sizes or add a region filter for your workload. The image must include automatic region/AD
discovery and already be published and pullable by the nodes.
The resource requests/limit are starting estimates; measure a representative scan
and adjust them. Recreate strategy avoids overlapping rollout collectors.
Configure Prometheus to scrape the Service's `metrics` port. A Grafana dashboard
is available in `dashboards/oci-compute-availability.json`.

Run the deployed collector continuously, without `--once`. For Prometheus running
inside the same cluster, a simple scrape configuration for namespace `monitoring`
is:

```yaml
scrape_configs:
  - job_name: oci-compute-availability
    scrape_interval: 30s
    static_configs:
      - targets:
          - oci-compute-availability.monitoring.svc.cluster.local:9109
```

Adapt the namespace, or use your existing ServiceMonitor/scrape discovery setup.
Import the dashboard JSON in Grafana and select this Prometheus data source.
Select a collector job and instance, then filter region, shape, OCPUs, and memory.
The dashboard keeps collector sources separate rather than combining customer
results. Data access remains governed by your Grafana and data-source permissions.

The first table shows only fresh AVAILABLE deployment options. The second shows
every observation, including unknown, unsupported, out-of-capacity and stale
rows. Both show per-check time, age, sizing, and available instance count when
OCI provides it. The default stale threshold is 900 seconds; adjust it to your
scan duration plus interval. Old counts do not make a stale row deployable.
Failed-stage details and availability history appear below the tables. No data
means no observations are currently available; it is not proof of zero capacity.

Collector connection, scan completeness, and individual observation freshness
are separate indicators. Each completed scan is published atomically, so the
previous snapshot remains visible while the next scan runs and continues aging.
Prometheus supplies history; the collector does not retain a durable database.

Validate dashboard queries with `make dashboard-test` (Python 3 and either
`promtool` or Docker). The fixtures exercise mixed success/failure, stale data,
unknown counts, and separation between collectors without requiring OCI access.

## Publishing

```bash
make image VERSION=v0.1.1     # local multiarchitecture OCI archive
make publish VERSION=v0.1.1   # tests, builds and pushes to GHCR
```

Alternatively, after creating the GitHub repository and pushing this project,
run **Actions → Publish container**. The workflow calls the same `make publish`
with repository `GITHUB_TOKEN` and `packages: write`. Local publishing requires
GitHub CLI authorized for `write:packages`, or `GHCR_TOKEN` and `GHCR_USERNAME`.
Never put tokens in Makefile arguments. Temporary registry credentials are removed
on exit. Both Linux AMD64 and ARM64 are built. Existing tags are reused; publish
changed source under a new version and deploy by digest when practical.
New GHCR package visibility must be checked independently of repository visibility;
make the package public for anonymous pulls or configure image-pull credentials.

## One global collector (v0.1.1+)

`--queries` also accepts a directory of regional `.json` files. One process loads
and validates all files in sorted order, then scans every configured region.
Each file retains the 1 MiB / 1,000-query bounds; the combined directory supports
up to 100,000 queries with globally unique names. Kubernetes projected ConfigMap
symlinks are supported. Mount one ConfigMap per regional file into a shared
projected volume so no ConfigMap exceeds Kubernetes' size limit.

The [examples/queries](examples/queries) directory contains separate Ashburn and
London query files, each with the same 36 E5/E6/E6.Ax sizing variants, globally unique
names, and automatic AD discovery. These 72 queries each expand across the ADs
in their region. Edit
the regions and shapes/sizes for your workload, then test the full directory:

```bash
./dist/collector --once --auth config \
  --profile DEFAULT --config-file "$HOME/.oci/config" \
  --queries examples/queries
```

For large global matrices use an explicit scan timeout appropriate to the query
count multiplied by subscribed regions and ADs per region, for example
`--queries /etc/capacity --timeout 1h --concurrency 6`. Regions are scanned with
bounded concurrency; requests within a region remain sequential;
`--interval 5m` is a pause after the entire global scan, not a five-minute freshness
guarantee. A failure marks the affected checks unknown and the scan incomplete;
successful observations remain visible with their individual timestamps.
Publish `make publish VERSION=v0.1.1` before deploying this directory configuration.

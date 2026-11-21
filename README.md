# OCI Compute availability

Standalone Go collector that periodically calls OCI's **CreateComputeCapacityReport**
API for explicitly configured regions, availability domains, shapes, shape sizes,
and optional fault domains. Project/repository name: `oci-compute-availability`.
It does not launch instances, reserve capacity, or scan other compartments.
Capacity reports use the tenancy root compartment.

## Run and build

Requires Go 1.26+, a C compiler for race tests, and Docker with Buildx for image
builds. No OCI access is needed to build or test.

```bash
make test
make build
cp queries.example.json queries.json
# Edit queries.json: supply your tenancy-specific AD names and desired shapes.
export OCI_TENANCY_OCID=ocid1.tenancy.oc1..REPLACE
./dist/collector --auth workload --queries queries.json --interval 5m
```

The example is an input template, not collected data. Each query has a unique
`name`, `region`, `availabilityDomain`, and `shape`. Flex shapes require explicit
`shapeConfig.ocpus` and `shapeConfig.memoryInGBs`. Optional `faultDomain` narrows
the request. Leave it absent for OCI's report across fault domains. Optional
shape configuration also supports `nvmes` and `baselineOcpuUtilization`.
Use distinct query names for different shape sizes. Results for overlapping
queries are alternative observations; do not add their available counts together.
Only shapes supported by the capacity-report API can be queried; the service's
API validation is authoritative. Unknown query-file fields are rejected.

The process scans immediately and waits five minutes **after completion** before
scanning again. `--interval` accepts Go durations (minimum 30s); `--timeout` bounds
a complete scan (default 10m). Queries run sequentially, reuse regional SDK clients,
and have bounded HTTP timeouts and retries. A single replica avoids duplicate
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
`OCI_RESOURCE_PRINCIPAL_REGION`. Request regions come from the query file.
`OCI_TENANCY_OCID` is required for every runtime identity.

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
./dist/collector --once --auth config --config-file /secure/path/oci.properties --queries queries.json
# Optional: --profile NAME
```

Restrict file/key permissions. Never commit, deploy, or bake them into an image.
Git ignores `*.properties`, `*.pem`, `*.key`, `.oci/`, and `oci-config*`.
The Docker build context allows only Go source, module files and Dockerfile.
Query JSON contains workload requirements, not credentials; mount it separately.

## Results and monitoring

HTTP listens on `:9109` (`--listen` overrides it):

- `/metrics`: Prometheus gauges with query, region, AD, shape and fault-domain labels.
- `/results`: latest complete snapshot, including requested shape sizes and raw OCI reports.
- `/healthz`: process liveness, independent of OCI collection success.

`--once` prints the snapshot as JSON and exits nonzero if incomplete.
Results are held in memory; Prometheus provides metric history. No database or
local result files are written by the service. Protect `/results` and `/metrics`
with your cluster network policy; these endpoints have no built-in HTTP authentication.

A failed query, malformed response, or unknown status suppresses all capacity
result series for that scan. Diagnostics remain available, including
`oci_capacity_collection_success`, `oci_capacity_collection_errors`, duration,
and attempt/last-success timestamps. Alert on failures and last-success age;
HTTP liveness alone does not establish successful collection.

`oci_capacity_available_count` is emitted only when OCI supplies a count: missing
counts are unknown, not zero. `oci_capacity_available` is one only for `AVAILABLE`;
`oci_capacity_status` distinguishes `OUT_OF_HOST_CAPACITY` and
`HARDWARE_NOT_SUPPORTED`. Query names identify the full requested configuration.
Capacity is an observation at request time, not a reservation or launch guarantee.
The sequential scan is not a simultaneous global snapshot.

See [CreateComputeCapacityReport](https://docs.oracle.com/en-us/iaas/tools/oci-cli/latest/oci_cli_docs/cmdref/compute/compute-capacity-report/create.html).

## Kubernetes deployment

`deploy/kubernetes.yaml` supplies one Deployment, ServiceAccount, query ConfigMap,
and Service. Replace the tenancy OCID, tenancy-specific AD, and image version;
configure workload IAM before applying into your chosen namespace. No credential
file is deployed. The image must already be published and pullable by the nodes.
The resource requests/limit are starting estimates; measure a representative scan
and adjust them. Recreate strategy avoids overlapping rollout collectors.
Configure Prometheus to scrape the Service's `metrics` port. A Grafana dashboard
is available in `dashboards/oci-compute-availability.json`.

## Publishing

```bash
make image VERSION=v0.1.0     # local multiarchitecture OCI archive
make publish VERSION=v0.1.0   # tests, builds and pushes to GHCR
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

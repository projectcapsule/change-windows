# Change Windows for Headlamp

This plugin adds a top-level **Change Windows** navigation entry for the Capsule
Scheduled Action Controller. It is independent of the Capsule Headlamp plugin.
Tested against Headlamp 0.45.0 with the pinned 0.14.0 plugin SDK.

- Weekly calendar with previous/next/today navigation and local-time labels.
- Searchable table of all ChangeWindows, active windows, next transitions,
  controller readiness, target kinds, and pending restoration counts.
- Detail pages for schedules, selectors, resource actions, admission and the CR.
- A **Maintenance windows** section on namespaced resource detail pages. It
  matches direct GVK/label targets and follows supported controlling workload
  owners, including Pod → ReplicaSet → Deployment.
- Explicit loading, missing-CRD, forbidden, and partial-selection states.

## Build and test

From the repository root:

```sh
devbox shell
task headlamp:install
task headlamp:check
task headlamp:package
```

Node 24.21.0 is pinned in Devbox and `.node-version`. `npm ci` uses the committed
lockfile. The build in `dist/` contains `main.js` and `package.json`; packaging
produces a local archive. Pull requests and pushes to `main` use the centralized
`projectcapsule/github-actions/headlamp-test` action and upload the build artifact.

The empty `go.mod` establishes a module boundary so controller Go tests, linters,
and generators do not traverse Go sources bundled inside npm dependencies.
This frontend has no Go packages and is built and tested with npm.

For development, use `task headlamp:serve` with a local Headlamp installation, or
mount/copy `dist/` as a separate `capsule-change-windows` directory beneath your
Headlamp server's configured plugin directory. Preserve other installed plugins.
Follow the [Headlamp build/install guide](https://headlamp.dev/docs/latest/development/plugins/building/)
for your desktop or in-cluster installation. An ephemeral plugin volume must be
populated again after pod replacement; use your deployment's plugin distribution
mechanism for a persistent installation.

The controller and its ChangeWindow CRD must be installed separately. The
plugin clearly reports an absent API; it does not install controllers or policies.

## Release automation

Pushing a `v*` semantic-version tag runs the same Headlamp checks. After those
checks and the controller's GitHub release succeed, the centralized
`headlamp-publish` action builds and attaches the plugin archive to that release.
The plugin version is taken from the tag; source package files are not committed
back to the repository.

Artifact Hub metadata and this README are published to the `headlamp-plugin`
branch under `capsule-change-windows/<version>/`. The workflow uses
`headlamp/artifacthub-pkg.yml` as its template, so the first release can create
the branch. The shared action writes the archive URL, checksum, version, and
creation timestamp. Publications to that branch are serialized across tags;
pull requests have read-only permissions and cannot publish.

The shared actions are pinned to a commit and use Node 24.21.0 with
`build-root: headlamp/`. Register the repository's `headlamp-plugin` branch in
Artifact Hub separately, following the
[Headlamp publishing guide](https://headlamp.dev/docs/latest/development/plugins/publishing/).
Repository ownership metadata belongs at the publication branch root. Branch
protection must permit the release job's `GITHUB_TOKEN` to push to that branch.

## Permissions and selection

Select one cluster to open the overview or an action. Requests use Headlamp's
authenticated user and explicit cluster context. The
plugin never reads journal Secrets or writes workloads. The optional
[`viewer-role.yaml`](deploy/viewer-role.yaml) lists its additional read permissions;
review and bind it to the intended users/groups. It includes cluster-scoped
ChangeWindow list/watch/get, namespace get, and keyed reads of supported
workload owners. The user's normal Headlamp permissions still govern Pod and
other resource detail pages. In restricted environments, grant parent-resource
reads using namespace-scoped Roles instead of the sample's cluster-wide grants.

Namespace selectors are ORed; labels within each selector use Kubernetes AND
semantics, including `NotIn` on absent labels. Namespace identity and target GVK
must match. Protected Kubernetes namespaces are excluded. If Helm installs the
controller outside `schedule-action-controller-system`, set the controller
namespace for that Headlamp cluster in the plugin's settings.

Owner traversal uses at most three same-namespace GETs and verifies every UID,
kind, and API version. Supported owners are apps/v1 Deployment, ReplicaSet,
StatefulSet, DaemonSet and batch/v1 Job/CronJob. Owners refresh every 30 seconds,
without overlapping requests; in-flight requests are cancelled on navigation.
Failure to read an owner is displayed as incomplete information, not an empty
success state. A window targeting a Deployment does not imply that Pod writes
are directly denied.

## Calendar semantics and limits

The preview interprets five-field cron and elapsed Go durations, merges adjacent
and overlapping windows, skips missing DST times, and includes repeated-hour
occurrences. Active state uses the browser clock, independent of delayed status;
controller readiness is displayed separately. Check clock synchronization when
comparing the preview with admission decisions.

Schedules are projected over bounded ranges with a 14-day margin. A transition
beyond the current preview is labelled accordingly. At most 250 matching actions
and 10,000 intervals per action are expanded; dense or invalid projections show an
explicit warning/unknown state. The table is paginated in groups of 50 and still
includes all actions. Filter by action name or kind to narrow the preview.
Continuous minutely schedules are collapsed into a single interval. Projection
results are memoized across clock ticks; resource detail pages first filter
policies by their namespace and target scope.

The view shows current selection, not durable participation or completed patches.
The controller snapshots participants at window start; later label changes or
new resources can differ from that snapshot. Captures and rendered patches remain
private to the controller's journal.

## Browser integration tests

The tests load the compiled bundle through the real Headlamp plugin loader while
intercepting Kubernetes/plugin responses in a fresh browser. They use fixtures;
no real credentials, policies or cluster writes are needed. Provide a reachable
Headlamp frontend, for example through a localhost port-forward:

```sh
kubectl --context YOUR_CONTEXT -n capsule-system \
  port-forward service/headlamp 18081:80 --address=127.0.0.1
# In another terminal, from the repository root:
cd headlamp
npx playwright install chromium
cd ..
HEADLAMP_URL=http://127.0.0.1:18081 task headlamp:test:browser
```

`CHROMIUM_EXECUTABLE` can select an existing compatible browser. Failed runs retain
traces/screenshots in `test-results/`. The suite covers registration, calendar and
details navigation, the Pod maintenance topic, permission/API errors, and narrow
viewports. These tests establish frontend integration, not controller execution.

Run `npm run bench` from this directory to measure schedule projection, including
one/100 policies and continuous minutely windows. Unit tests cover DST, merged
boundaries, selector semantics, resource replacement, isolation, and UI states.

//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	nb "github.com/evenh/cluster-api-ipam-provider-netbox/internal/netbox"
	"github.com/evenh/cluster-api-ipam-provider-netbox/internal/version"
)

const (
	chainsawScenarioRoot   = "test/e2e/scenarios"
	defaultOwnershipTag    = "cluster-api-ipam-provider-netbox"
	defaultClaimUIDField   = "cluster_api_claim_uid"
	defaultChainsawTimeout = 2 * time.Minute
	netboxSuperuserToken   = "capi-netbox-e2e-token-0123456789abcdef"
	netboxAPITokenPepper   = "cluster-api-ipam-provider-netbox-e2e-pepper-0123456789abcdef"
	netboxImage            = "netboxcommunity/netbox:v4.5.4"
	postgresImage          = "postgres:18-alpine"
	valkeyImage            = "valkey/valkey:9-alpine"
	netboxCustomFieldName  = defaultClaimUIDField
	netboxCustomFieldLabel = "Cluster API Claim UID"
	netboxSecretKey        = "cluster-api-ipam-provider-netbox-secret-key-for-e2e-tests-0123456789abcdef"
	kindClusterName        = "netbox-ipam-e2e"
	managerStartupWait     = 5 * time.Second
	managerShutdownGrace   = 5 * time.Second
	resourceCleanupTimeout = 2 * time.Minute
	resourcePollInterval   = 2 * time.Second
	netboxStartupTimeout   = 10 * time.Minute
	netboxTokenPollTimeout = 30 * time.Second
	netboxHTTPTimeout      = 30 * time.Second
)

// KindContextName returns the kubeconfig context name kind generates for a
// cluster named clusterName.
func KindContextName(clusterName string) string {
	return "kind-" + clusterName
}

var netboxAPITokenKeyPattern = regexp.MustCompile(`API Token: ([A-Za-z0-9]+)`)
var errNetBoxAPITokenKeyNotFound = errors.New("NetBox v2 API token key not found in container logs")

type scenario struct {
	name                  string
	dir                   string
	namespace             string
	poolName              string
	claimName             string
	expectedDNSName       string
	expectedStatus        string
	expectedTags          []string
	expectedCustomFields  map[string]string
	values                map[string]string
	extraCleanupResources []cleanupResource
}

type cleanupResource struct {
	kind      string
	name      string
	namespace string
}

type netBoxAdminClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type netBoxListResponse[T any] struct {
	Results []T `json:"results"`
}

type NetBoxPrefix struct {
	ID     int32  `json:"id"`
	Prefix string `json:"prefix"`
}

type NetBoxIPAddress struct {
	ID           int32          `json:"id"`
	Address      string         `json:"address"`
	DNSName      string         `json:"dns_name"`
	Status       *netBoxStatus  `json:"status,omitempty"`
	Tags         []netBoxTag    `json:"tags,omitempty"`
	CustomFields map[string]any `json:"custom_fields,omitempty"`
}

type netBoxStatus struct {
	Value string `json:"value"`
}

type netBoxTag struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// Environment provisions (or wraps an already-provisioned) NetBox + kind e2e stack.
//
// Two lifecycles use this type: TestE2E provisions a throwaway environment for the
// duration of a single test run and tears it down when done. The e2eup/e2edown tools
// (see test/e2e/cmd) provision a long-lived environment a human can point tests or
// chainsaw at repeatedly, and tear it down explicitly later.
type Environment struct {
	ctx            context.Context
	ProjectDir     string
	WorkDir        string
	KubeconfigPath string
	ClusterName    string
	netboxURL      string
	netboxToken    string
	netboxTokenRaw string
	managerCmd     *exec.Cmd
	managerLogPath string
	managerDone    chan error
	netboxClient   *netBoxAdminClient

	dockerNetwork     *testcontainers.DockerNetwork
	postgresContainer *postgres.PostgresContainer
	valkeyContainer   testcontainers.Container
	netboxContainer   testcontainers.Container
}

// NewEnvironment creates an environment rooted at workDir, ready for Setup.
func NewEnvironment(ctx context.Context, workDir, projectDir string) *Environment {
	return &Environment{
		ctx:            ctx,
		ProjectDir:     projectDir,
		WorkDir:        workDir,
		KubeconfigPath: filepath.Join(workDir, "kubeconfig"),
		ClusterName:    kindClusterName,
		netboxTokenRaw: netboxSuperuserToken,
		managerDone:    make(chan error, 1),
	}
}

// EnvironmentFromState wraps an already-provisioned environment (created by e2eup)
// so tests can run against it without provisioning or tearing anything down.
func EnvironmentFromState(ctx context.Context, workDir string, state *EnvironmentState) (*Environment, error) {
	client, err := newNetBoxAPIClient(state.NetBoxURL, state.NetBoxToken)
	if err != nil {
		return nil, err
	}
	return &Environment{
		ctx:            ctx,
		ProjectDir:     state.ProjectDir,
		WorkDir:        workDir,
		KubeconfigPath: state.KubeconfigPath,
		ClusterName:    state.ClusterName,
		netboxURL:      state.NetBoxURL,
		netboxToken:    state.NetBoxToken,
		managerLogPath: state.ManagerLogPath,
		netboxClient:   client,
	}, nil
}

func (e *Environment) Setup() error {
	if err := e.startNetBox(); err != nil {
		return err
	}
	for _, fieldName := range []string{netboxCustomFieldName, "source", "owner"} {
		if err := e.ensureCustomField(e.ctx, fieldName); err != nil {
			return err
		}
	}
	if err := e.createKindCluster(e.ctx); err != nil {
		return err
	}
	if err := e.installCRDs(e.ctx); err != nil {
		return err
	}
	if err := e.startManager(e.ctx); err != nil {
		return err
	}
	return nil
}

// Cleanup tears down everything Setup provisioned. Do not call this on an
// environment obtained from EnvironmentFromState; use the e2edown tool instead.
func (e *Environment) Cleanup() {
	if e.managerCmd != nil && e.managerCmd.Process != nil {
		_ = e.managerCmd.Process.Kill()
		select {
		case <-e.managerDone:
		case <-time.After(managerShutdownGrace):
		}
	}
	if e.netboxContainer != nil {
		_ = e.netboxContainer.Terminate(e.ctx)
	}
	if e.valkeyContainer != nil {
		_ = e.valkeyContainer.Terminate(e.ctx)
	}
	if e.postgresContainer != nil {
		_ = e.postgresContainer.Terminate(e.ctx)
	}
	if e.dockerNetwork != nil {
		_ = e.dockerNetwork.Remove(e.ctx)
	}
	if e.ClusterName != "" {
		_ = e.runCmd(e.ctx, e.ProjectDir, "kind", "delete", "cluster", "--name", e.ClusterName)
	}
}

func (e *Environment) NetBoxURL() string {
	return e.netboxURL
}

func (e *Environment) NetBoxToken() string {
	return e.netboxToken
}

func (e *Environment) ManagerLogPath() string {
	return e.managerLogPath
}

// ManagerPID returns the PID of the manager process started by Setup, or 0 if
// this environment did not start one itself (e.g. it came from EnvironmentFromState).
func (e *Environment) ManagerPID() int {
	if e.managerCmd == nil || e.managerCmd.Process == nil {
		return 0
	}
	return e.managerCmd.Process.Pid
}

func (e *Environment) DockerNetworkID() string {
	if e.dockerNetwork == nil {
		return ""
	}
	return e.dockerNetwork.ID
}

func (e *Environment) PostgresContainerID() string {
	if e.postgresContainer == nil {
		return ""
	}
	return e.postgresContainer.GetContainerID()
}

func (e *Environment) ValkeyContainerID() string {
	if e.valkeyContainer == nil {
		return ""
	}
	return e.valkeyContainer.GetContainerID()
}

func (e *Environment) NetBoxContainerID() string {
	if e.netboxContainer == nil {
		return ""
	}
	return e.netboxContainer.GetContainerID()
}

func (e *Environment) startNetBox() error {
	dockerNetwork, err := tcnetwork.New(e.ctx)
	if err != nil {
		return fmt.Errorf("create docker network: %w", err)
	}
	e.dockerNetwork = dockerNetwork

	postgresContainer, err := postgres.Run(e.ctx,
		postgresImage,
		postgres.WithDatabase("netbox"),
		postgres.WithUsername("netbox"),
		postgres.WithPassword("netbox"),
		tcnetwork.WithNetwork([]string{"postgres"}, dockerNetwork),
	)
	if err != nil {
		return fmt.Errorf("start postgres: %w", err)
	}
	e.postgresContainer = postgresContainer

	valkeyContainer, err := testcontainers.Run(e.ctx,
		valkeyImage,
		testcontainers.WithExposedPorts("6379/tcp"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("6379/tcp")),
		tcnetwork.WithNetwork([]string{"valkey"}, dockerNetwork),
	)
	if err != nil {
		return fmt.Errorf("start valkey: %w", err)
	}
	e.valkeyContainer = valkeyContainer

	netboxContainer, err := testcontainers.Run(e.ctx,
		netboxImage,
		testcontainers.WithExposedPorts("8080/tcp"),
		testcontainers.WithEnv(map[string]string{
			"DB_NAME":             "netbox",
			"DB_USER":             "netbox",
			"DB_PASSWORD":         "netbox",
			"DB_HOST":             "postgres",
			"DB_PORT":             "5432",
			"REDIS_HOST":          "valkey",
			"REDIS_PORT":          "6379",
			"REDIS_CACHE_HOST":    "valkey",
			"REDIS_CACHE_PORT":    "6379",
			"SECRET_KEY":          netboxSecretKey,
			"API_TOKEN_PEPPER_1":  netboxAPITokenPepper,
			"DB_WAIT_DEBUG":       "1",
			"SUPERUSER_NAME":      "admin",
			"SUPERUSER_EMAIL":     "admin@example.com",
			"SUPERUSER_PASSWORD":  "admin",
			"SUPERUSER_API_TOKEN": e.netboxTokenRaw,
			"ALLOWED_HOSTS":       "*",
		}),
		testcontainers.WithWaitStrategyAndDeadline(netboxStartupTimeout,
			wait.ForHTTP("/api/").
				WithPort("8080/tcp").
				WithAllowInsecure(true).
				WithStatusCodeMatcher(func(code int) bool {
					return code == http.StatusOK || code == http.StatusFound || code == http.StatusForbidden
				}).
				WithStartupTimeout(netboxStartupTimeout),
		),
		tcnetwork.WithNetwork([]string{"netbox"}, dockerNetwork),
	)
	if err != nil {
		return fmt.Errorf("start netbox: %w", err)
	}
	e.netboxContainer = netboxContainer

	host, err := netboxContainer.Host(e.ctx)
	if err != nil {
		return fmt.Errorf("netbox host: %w", err)
	}
	port, err := netboxContainer.MappedPort(e.ctx, "8080/tcp")
	if err != nil {
		return fmt.Errorf("map netbox port: %w", err)
	}
	e.netboxURL = "http://" + net.JoinHostPort(host, port.Port())
	e.netboxToken, err = e.resolveNetBoxAPIToken(e.ctx)
	if err != nil {
		return err
	}

	client, err := newNetBoxAPIClient(e.netboxURL, e.netboxToken)
	if err != nil {
		return err
	}
	e.netboxClient = client

	return nil
}

func (e *Environment) resolveNetBoxAPIToken(ctx context.Context) (string, error) {
	deadline := time.Now().Add(netboxTokenPollTimeout)
	for time.Now().Before(deadline) {
		key, err := e.netboxAPITokenKey(ctx)
		if err == nil {
			return nb.ComposeV2Token(key, e.netboxTokenRaw), nil
		}
		if !errors.Is(err, errNetBoxAPITokenKeyNotFound) {
			return "", err
		}
		time.Sleep(time.Second)
	}

	return "", fmt.Errorf("resolve NetBox API token: %w", errNetBoxAPITokenKeyNotFound)
}

func (e *Environment) netboxAPITokenKey(ctx context.Context) (string, error) {
	logs, err := e.netboxContainer.Logs(ctx)
	if err != nil {
		return "", fmt.Errorf("read NetBox container logs: %w", err)
	}
	defer logs.Close()

	logData, err := io.ReadAll(logs)
	if err != nil {
		return "", fmt.Errorf("read NetBox container logs payload: %w", err)
	}

	matches := netboxAPITokenKeyPattern.FindAllStringSubmatch(string(logData), -1)
	if len(matches) == 0 {
		return "", errNetBoxAPITokenKeyNotFound
	}

	return matches[len(matches)-1][1], nil
}

func (e *Environment) createKindCluster(ctx context.Context) error {
	_ = e.runCmd(ctx, e.ProjectDir, "kind", "delete", "cluster", "--name", e.ClusterName)
	return e.runCmd(
		ctx,
		e.ProjectDir,
		"kind",
		"create",
		"cluster",
		"--name",
		e.ClusterName,
		"--kubeconfig",
		e.KubeconfigPath,
	)
}

func (e *Environment) installCRDs(ctx context.Context) error {
	capiModuleDir, err := clusterAPIModuleDir()
	if err != nil {
		return err
	}

	capiCRDs := []string{
		filepath.Join(capiModuleDir, "config", "crd", "bases", "cluster.x-k8s.io_clusters.yaml"),
		filepath.Join(capiModuleDir, "config", "crd", "bases", "ipam.cluster.x-k8s.io_ipaddressclaims.yaml"),
		filepath.Join(capiModuleDir, "config", "crd", "bases", "ipam.cluster.x-k8s.io_ipaddresses.yaml"),
	}

	args := []string{"--kubeconfig", e.KubeconfigPath, "apply"}
	for _, crd := range capiCRDs {
		args = append(args, "-f", crd)
	}
	args = append(args, "-f", filepath.Join(e.ProjectDir, "config", "crd", "bases"))
	err = e.runCmd(ctx, e.ProjectDir, "kubectl", args...)
	if err != nil {
		return err
	}

	for _, crdName := range []string{
		"clusters.cluster.x-k8s.io",
		"ipaddressclaims.ipam.cluster.x-k8s.io",
		"ipaddresses.ipam.cluster.x-k8s.io",
		"netboxippools.ipam.cluster.x-k8s.io",
		"globalnetboxippools.ipam.cluster.x-k8s.io",
	} {
		err = e.runCmd(ctx, e.ProjectDir,
			"kubectl", "--kubeconfig", e.KubeconfigPath,
			"wait", "--for=condition=Established", "--timeout=2m",
			fmt.Sprintf("crd/%s", crdName),
		)
		if err != nil {
			return err
		}
	}

	return nil
}

// startManager builds the controller-manager binary and execs it directly (rather than
// via `go run`) so its PID is the actual manager process — important for e2eup, where a
// human may later kill it by PID from the persisted environment state.
func (e *Environment) startManager(ctx context.Context) error {
	binaryPath := filepath.Join(e.WorkDir, "manager")
	buildCmd := exec.CommandContext(ctx, "go", "build", "-o", binaryPath, "./cmd/main.go")
	buildCmd.Dir = e.ProjectDir
	buildCmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(e.ProjectDir, ".cache", "go-build"))
	if output, err := buildCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build manager: %w\n%s", err, output)
	}

	logPath := filepath.Join(e.WorkDir, "manager.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("create manager log: %w", err)
	}
	e.managerLogPath = logPath

	cmd := exec.Command(binaryPath,
		"--leader-elect=false",
		"--metrics-bind-address=0",
		"--health-probe-bind-address=0",
	)
	cmd.Dir = e.ProjectDir
	cmd.Env = append(os.Environ(), "KUBECONFIG="+e.KubeconfigPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	err = cmd.Start()
	if err != nil {
		_ = logFile.Close()
		return fmt.Errorf("start manager: %w", err)
	}
	e.managerCmd = cmd

	go func() {
		defer logFile.Close()
		e.managerDone <- cmd.Wait()
	}()

	time.Sleep(managerStartupWait)
	select {
	case err = <-e.managerDone:
		return fmt.Errorf("manager exited early: %w\n%s", err, e.readManagerLog())
	default:
	}

	return nil
}

func (e *Environment) readManagerLog() string {
	if e.managerLogPath == "" {
		return ""
	}
	data, err := os.ReadFile(e.managerLogPath)
	if err != nil {
		return ""
	}
	return string(data)
}

func (e *Environment) RunChainsawScenario(ctx context.Context, scenarioDir string, values map[string]string) error {
	valuesPath := filepath.Join(e.WorkDir, filepath.Base(scenarioDir)+"-values.yaml")
	if err := os.WriteFile(valuesPath, []byte(renderValues(values)), 0o600); err != nil {
		return fmt.Errorf("write chainsaw values: %w", err)
	}

	output, err := e.runCmdOutput(ctx, e.ProjectDir, []string{"KUBECONFIG=" + e.KubeconfigPath},
		"go",
		"tool", "chainsaw",
		"test",
		"--config", filepath.Join("test", "e2e", "chainsaw.yaml"),
		"--kube-context", KindContextName(e.ClusterName),
		"--skip-delete",
		"--values", valuesPath,
		scenarioDir,
	)
	if err != nil {
		var details strings.Builder
		details.WriteString(output)
		select {
		case managerErr := <-e.managerDone:
			details.WriteString("\nmanager exited:\n")
			fmt.Fprintf(&details, "%v\n", managerErr)
		default:
		}
		if managerOutput := strings.TrimSpace(e.readManagerLog()); managerOutput != "" {
			details.WriteString("\nmanager output:\n")
			details.WriteString(managerOutput)
			details.WriteByte('\n')
		}
		if clusterState := e.debugClusterState(ctx); strings.TrimSpace(clusterState) != "" {
			details.WriteString("\ncluster state:\n")
			details.WriteString(clusterState)
		}
		return fmt.Errorf("chainsaw failed: %w\n%s", err, details.String())
	}
	return nil
}

func (e *Environment) debugClusterState(ctx context.Context) string {
	commands := [][]string{
		{
			"get",
			"ipaddressclaims.ipam.cluster.x-k8s.io,ipaddresses.ipam.cluster.x-k8s.io,netboxippools.ipam.cluster.x-k8s.io,globalnetboxippools.ipam.cluster.x-k8s.io",
			"-A",
			"-oyaml",
		},
		{"get", "events", "-A", "--sort-by=.metadata.creationTimestamp"},
	}

	var out strings.Builder
	for _, args := range commands {
		output, err := e.runCmdOutput(
			ctx,
			e.ProjectDir,
			nil,
			"kubectl",
			append([]string{"--kubeconfig", e.KubeconfigPath}, args...)...)
		if err != nil {
			out.WriteString(strings.Join(args, " "))
			out.WriteString(":\n")
			out.WriteString(err.Error())
			out.WriteByte('\n')
			if strings.TrimSpace(output) != "" {
				out.WriteString(output)
				out.WriteByte('\n')
			}
			continue
		}
		if strings.TrimSpace(output) == "" {
			continue
		}
		out.WriteString(strings.Join(args, " "))
		out.WriteString(":\n")
		out.WriteString(output)
		if !strings.HasSuffix(output, "\n") {
			out.WriteByte('\n')
		}
	}

	return out.String()
}

func (e *Environment) GetClaimUID(ctx context.Context, namespace, claimName string) (string, error) {
	output, err := e.runCmdOutput(ctx, e.ProjectDir, nil,
		"kubectl",
		"--kubeconfig", e.KubeconfigPath,
		"-n", namespace,
		"get", "ipaddressclaim", claimName,
		"-o", "jsonpath={.metadata.uid}",
	)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func (e *Environment) CleanupScenario(ctx context.Context, scenario scenario) error {
	if err := e.runCmd(ctx, e.ProjectDir,
		"kubectl",
		"--kubeconfig", e.KubeconfigPath,
		"delete", "namespace", scenario.namespace,
		"--wait=false",
		"--ignore-not-found=true",
	); err != nil {
		return err
	}
	if err := e.waitForNamespaceDeleted(ctx, scenario.namespace, resourceCleanupTimeout); err != nil {
		return err
	}

	for _, resource := range scenario.extraCleanupResources {
		args := []string{
			"--kubeconfig", e.KubeconfigPath,
			"delete", resource.kind, resource.name,
			"--wait=true",
			"--timeout=2m",
			"--ignore-not-found=true",
		}
		if resource.namespace != "" {
			args = append(
				[]string{
					"--kubeconfig",
					e.KubeconfigPath,
					"-n",
					resource.namespace,
					"delete",
					resource.kind,
					resource.name,
				},
				args[4:]...)
		}
		if err := e.runCmd(ctx, e.ProjectDir, "kubectl", args...); err != nil {
			return err
		}
	}

	return nil
}

func (e *Environment) DeleteClaim(ctx context.Context, namespace, name string) error {
	return e.runCmd(ctx, e.ProjectDir,
		"kubectl",
		"--kubeconfig", e.KubeconfigPath,
		"-n", namespace,
		"delete", "ipaddressclaims.ipam.cluster.x-k8s.io", name,
		"--wait=false",
		"--ignore-not-found=true",
	)
}

func (e *Environment) waitForNamespaceDeleted(ctx context.Context, namespace string, timeout time.Duration) error {
	return e.waitForResourceDeleted(ctx, "", "namespace", namespace, timeout)
}

func (e *Environment) waitForResourceDeleted(
	ctx context.Context,
	namespace, resource, name string,
	timeout time.Duration,
) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		args := []string{
			"--kubeconfig", e.KubeconfigPath,
		}
		if namespace != "" {
			args = append(args, "-n", namespace)
		}
		args = append(args,
			"get", resource, name,
			"--ignore-not-found",
			"-o", "name",
		)
		output, err := e.runCmdOutput(ctx, e.ProjectDir, nil, "kubectl", args...)
		if err != nil {
			return err
		}
		if strings.TrimSpace(output) == "" {
			return nil
		}
		time.Sleep(resourcePollInterval)
	}

	scope := name
	if namespace != "" {
		scope = namespace + "/" + name
	}
	return fmt.Errorf("timed out waiting for %s %q deletion", resource, scope)
}

func (e *Environment) failureDetails(ctx context.Context) string {
	var details strings.Builder
	if managerOutput := strings.TrimSpace(e.readManagerLog()); managerOutput != "" {
		details.WriteString("manager output:\n")
		details.WriteString(managerOutput)
		details.WriteByte('\n')
	}
	if clusterState := strings.TrimSpace(e.debugClusterState(ctx)); clusterState != "" {
		details.WriteString("\ncluster state:\n")
		details.WriteString(clusterState)
	}
	if netBoxState := strings.TrimSpace(e.debugNetBoxState(ctx)); netBoxState != "" {
		details.WriteString("\nnetbox state:\n")
		details.WriteString(netBoxState)
	}
	return details.String()
}

func (e *Environment) debugNetBoxState(ctx context.Context) string {
	result, err := e.netboxClient.listIPAddresses(ctx, url.Values{})
	if err != nil {
		return fmt.Sprintf("list ip addresses: %v", err)
	}
	if len(result.Results) == 0 {
		return "no ip addresses"
	}

	var out strings.Builder
	for _, item := range result.Results {
		fmt.Fprintf(&out, "- id=%d address=%s dns=%s tags=%v customFields=%v\n",
			item.ID,
			item.Address,
			item.DNSName,
			extractTagNames(item.Tags),
			item.CustomFields,
		)
	}
	return out.String()
}

func extractTagNames(tags []netBoxTag) []string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		out = append(out, tag.Name)
	}
	return out
}

func (e *Environment) CreatePrefix(ctx context.Context, cidr string) (*NetBoxPrefix, error) {
	return e.netboxClient.createPrefix(ctx, cidr)
}

//nolint:nilnil // nil, nil is this test helper's "not found" result; callers check the pointer.
func (e *Environment) FindIPAddressByClaimUID(
	ctx context.Context,
	ownershipTag, fieldName, claimUID string,
) (*NetBoxIPAddress, error) {
	query := url.Values{}
	query.Set("tag", ownershipTag)
	result, err := e.netboxClient.listIPAddresses(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list ip addresses: %w", err)
	}
	for _, item := range result.Results {
		if fmt.Sprint(item.CustomFields[fieldName]) == claimUID {
			return &item, nil
		}
	}
	return nil, nil
}

func (e *Environment) WaitForIPAddressDeleted(
	ctx context.Context,
	ownershipTag, fieldName, claimUID string,
	timeout time.Duration,
) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ipAddress, err := e.FindIPAddressByClaimUID(ctx, ownershipTag, fieldName, claimUID)
		if err != nil {
			return err
		}
		if ipAddress == nil {
			return nil
		}
		time.Sleep(resourcePollInterval)
	}
	return fmt.Errorf("timed out waiting for NetBox IP cleanup for claim UID %q", claimUID)
}

func (e *Environment) ensureCustomField(ctx context.Context, fieldName string) error {
	return e.netboxClient.ensureCustomField(ctx, fieldName)
}

func (e *Environment) runCmd(ctx context.Context, dir string, name string, args ...string) error {
	output, err := e.runCmdOutput(ctx, dir, nil, name, args...)
	if err == nil {
		return nil
	}

	commandLine := strings.TrimSpace(strings.Join(append([]string{name}, args...), " "))
	if strings.TrimSpace(output) == "" {
		return fmt.Errorf("%s: %w", commandLine, err)
	}

	return fmt.Errorf("%s: %w\n%s", commandLine, err, output)
}

func (e *Environment) runCmdOutput(
	ctx context.Context,
	dir string,
	env []string,
	name string,
	args ...string,
) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func newNetBoxAPIClient(baseURL, token string) (*netBoxAdminClient, error) {
	if _, err := url.ParseRequestURI(baseURL); err != nil {
		return nil, fmt.Errorf("parse netbox url: %w", err)
	}

	return &netBoxAdminClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: &http.Client{Timeout: netboxHTTPTimeout},
	}, nil
}

func goModCache() string {
	if value := os.Getenv("GOMODCACHE"); value != "" {
		return value
	}
	output, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		return filepath.Join(os.Getenv("HOME"), "go", "pkg", "mod")
	}
	return strings.TrimSpace(string(output))
}

func clusterAPIModuleDir() (string, error) {
	output, err := exec.Command(
		"go", "list", "-m", "-f", "{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}",
		"sigs.k8s.io/cluster-api",
	).Output()
	if err != nil {
		return "", fmt.Errorf("resolve sigs.k8s.io/cluster-api module version: %w", err)
	}
	version := strings.TrimSpace(string(output))
	return filepath.Join(goModCache(), "sigs.k8s.io/cluster-api@"+version), nil
}

// ResolveProjectDir walks up from start looking for the module root (the directory
// containing go.mod).
func ResolveProjectDir(start string) (string, error) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from %q", start)
		}
		dir = parent
	}
}

func renderValues(values map[string]string) string {
	var builder strings.Builder
	keys := []string{"namespace", "netboxURL", "netboxToken", "prefixID", "prefixCIDR"}
	for _, key := range keys {
		if value, ok := values[key]; ok {
			if key == "prefixID" {
				fmt.Fprintf(&builder, "%s: %s\n", key, value)
				continue
			}
			fmt.Fprintf(&builder, "%s: %q\n", key, value)
		}
	}
	return builder.String()
}

func (c *netBoxAdminClient) createPrefix(ctx context.Context, cidr string) (*NetBoxPrefix, error) {
	request := map[string]any{
		"prefix": cidr,
		"status": "active",
	}
	var prefix NetBoxPrefix
	if err := c.do(ctx, http.MethodPost, "/api/ipam/prefixes/", nil, request, &prefix, http.StatusCreated); err != nil {
		return nil, fmt.Errorf("create prefix %q: %w", cidr, err)
	}
	return &prefix, nil
}

func (c *netBoxAdminClient) listIPAddresses(
	ctx context.Context,
	query url.Values,
) (*netBoxListResponse[NetBoxIPAddress], error) {
	var result netBoxListResponse[NetBoxIPAddress]
	if err := c.do(ctx, http.MethodGet, "/api/ipam/ip-addresses/", query, nil, &result, http.StatusOK); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *netBoxAdminClient) ensureCustomField(ctx context.Context, fieldName string) error {
	query := url.Values{}
	query.Set("name", fieldName)
	query.Set("object_type", "ipam.ipaddress")

	var result netBoxListResponse[map[string]any]
	if err := c.do(ctx, http.MethodGet, "/api/extras/custom-fields/", query, nil, &result, http.StatusOK); err != nil {
		return fmt.Errorf("list custom fields: %w", err)
	}
	if len(result.Results) > 0 {
		return nil
	}

	request := map[string]any{
		"object_types": []string{"ipam.ipaddress"},
		"type":         "text",
		"name":         fieldName,
		"label":        netboxCustomFieldLabel,
	}
	if err := c.do(
		ctx,
		http.MethodPost,
		"/api/extras/custom-fields/",
		nil,
		request,
		nil,
		http.StatusCreated,
	); err != nil {
		return fmt.Errorf("create custom field %q: %w", fieldName, err)
	}
	return nil
}

func (c *netBoxAdminClient) do(
	ctx context.Context,
	method, path string,
	query url.Values,
	request any,
	response any,
	expectedStatus ...int,
) error {
	var body io.Reader
	if request != nil {
		payload, err := json.Marshal(request)
		if err != nil {
			return fmt.Errorf("marshal %s %s request: %w", method, path, err)
		}
		body = bytes.NewReader(payload)
	}

	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("build %s %s request: %w", method, path, err)
	}
	httpReq.Header.Set("Authorization", nb.AuthorizationHeaderValue(c.token))
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", version.UserAgent())
	if request != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return fmt.Errorf("read %s %s response: %w", method, path, err)
	}
	if !containsStatus(expectedStatus, httpResp.StatusCode) {
		return fmt.Errorf("%s %s returned %s: %s", method, path, httpResp.Status, strings.TrimSpace(string(respBody)))
	}
	if response == nil || len(respBody) == 0 {
		return nil
	}
	err = json.Unmarshal(respBody, response)
	if err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, path, err)
	}
	return nil
}

func containsStatus(statuses []int, status int) bool {
	return slices.Contains(statuses, status)
}

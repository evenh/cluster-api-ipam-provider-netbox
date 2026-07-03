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
	"strconv"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"

	nb "github.com/evenh/cluster-api-ipam-provider-netbox/internal/netbox"
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
	kindContextName        = "kind-netbox-ipam-e2e"
	managerStartupWait     = 5 * time.Second
	resourceCleanupTimeout = 2 * time.Minute
)

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

type netBoxPrefix struct {
	ID     int32  `json:"id"`
	Prefix string `json:"prefix"`
}

type netBoxIPAddress struct {
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

//nolint:tparallel // scenarios share one kind cluster, NetBox instance, and manager process; run sequentially.
func TestE2E(t *testing.T) {
	t.Parallel()

	if testing.Short() {
		t.Skip("skipping e2e in short mode")
	}

	projectDir, setupErr := os.Getwd()
	if setupErr != nil {
		t.Fatalf("getwd: %v", setupErr)
	}
	projectDir, setupErr = resolveProjectDir(projectDir)
	if setupErr != nil {
		t.Fatalf("resolve project dir: %v", setupErr)
	}

	ctx := t.Context()

	env := newE2EEnvironment(ctx, t, projectDir)
	defer env.Cleanup()

	if setupErr = env.Setup(); setupErr != nil {
		t.Fatalf("setup e2e environment: %v", setupErr)
	}

	namespacedPrefix, setupErr := env.CreatePrefix(ctx, "10.203.1.0/24")
	if setupErr != nil {
		t.Fatalf("create namespaced prefix: %v", setupErr)
	}
	globalPrefix, setupErr := env.CreatePrefix(ctx, "10.203.2.0/24")
	if setupErr != nil {
		t.Fatalf("create global prefix: %v", setupErr)
	}

	scenarios := []scenario{
		{
			name:            "namespaced-pool-by-prefix-id",
			dir:             filepath.Join(chainsawScenarioRoot, "namespaced-ipv4"),
			namespace:       "e2e-namespaced",
			poolName:        "netbox-pool",
			claimName:       "claim",
			expectedDNSName: "claimed.example.com",
			expectedStatus:  "active",
			expectedTags: []string{
				defaultOwnershipTag,
				"claim-override",
			},
			expectedCustomFields: map[string]string{
				"source": "chainsaw",
				"owner":  "namespaced",
			},
			values: map[string]string{
				"namespace":   "e2e-namespaced",
				"netboxURL":   env.NetBoxURL(),
				"netboxToken": env.NetBoxToken(),
				"prefixID":    strconv.Itoa(int(namespacedPrefix.ID)),
			},
		},
		{
			name:            "global-pool-by-cidr",
			dir:             filepath.Join(chainsawScenarioRoot, "global-ipv4"),
			namespace:       "e2e-global",
			poolName:        "global-netbox-pool",
			claimName:       "claim",
			expectedDNSName: "global.example.com",
			expectedStatus:  "reserved",
			expectedTags: []string{
				defaultOwnershipTag,
				"global-default",
			},
			expectedCustomFields: map[string]string{
				"source": "global",
			},
			values: map[string]string{
				"namespace":   "e2e-global",
				"netboxURL":   env.NetBoxURL(),
				"netboxToken": env.NetBoxToken(),
				"prefixCIDR":  globalPrefix.Prefix,
			},
			extraCleanupResources: []cleanupResource{{
				kind: "globalnetboxippool.ipam.cluster.x-k8s.io",
				name: "global-netbox-pool",
			}},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			err := env.RunChainsawScenario(ctx, scenario.dir, scenario.values)
			if err != nil {
				t.Fatalf("run chainsaw scenario: %v", err)
			}

			claimUID, err := env.GetClaimUID(ctx, scenario.namespace, scenario.claimName)
			if err != nil {
				t.Fatalf("get claim uid: %v", err)
			}

			ipAddress, err := env.FindIPAddressByClaimUID(ctx, defaultOwnershipTag, defaultClaimUIDField, claimUID)
			if err != nil {
				t.Fatalf("find ip by claim uid: %v", err)
			}
			if ipAddress == nil {
				t.Fatal("expected NetBox IP to exist after scenario")
			}

			env.AssertIPAddress(t, ipAddress, scenario)

			err = env.DeleteClaim(ctx, scenario.namespace, scenario.claimName)
			if err != nil {
				t.Fatalf("delete claim: %v\n%s", err, env.failureDetails(ctx))
			}
			err = env.WaitForIPAddressDeleted(
				ctx,
				defaultOwnershipTag,
				defaultClaimUIDField,
				claimUID,
				resourceCleanupTimeout,
			)
			if err != nil {
				t.Fatalf("wait for NetBox IP deletion: %v\n%s", err, env.failureDetails(ctx))
			}
			err = env.waitForResourceDeleted(
				ctx,
				scenario.namespace,
				"ipaddressclaims.ipam.cluster.x-k8s.io",
				scenario.claimName,
				resourceCleanupTimeout,
			)
			if err != nil {
				t.Fatalf("wait for claim deletion: %v\n%s", err, env.failureDetails(ctx))
			}
			err = env.waitForResourceDeleted(
				ctx,
				scenario.namespace,
				"ipaddresses.ipam.cluster.x-k8s.io",
				scenario.claimName,
				resourceCleanupTimeout,
			)
			if err != nil {
				t.Fatalf("wait for address deletion: %v\n%s", err, env.failureDetails(ctx))
			}
			err = env.CleanupScenario(ctx, scenario)
			if err != nil {
				t.Fatalf("cleanup scenario: %v\n%s", err, env.failureDetails(ctx))
			}
		})
	}
}

type environment struct {
	t              *testing.T
	ctx            context.Context
	projectDir     string
	workDir        string
	kubeconfigPath string
	clusterName    string
	netboxURL      string
	netboxToken    string
	netboxTokenRaw string
	managerCmd     *exec.Cmd
	managerOutput  bytes.Buffer
	managerDone    chan error
	netboxClient   *netBoxAdminClient

	dockerNetwork     *testcontainers.DockerNetwork
	postgresContainer *postgres.PostgresContainer
	valkeyContainer   testcontainers.Container
	netboxContainer   testcontainers.Container
}

func newE2EEnvironment(ctx context.Context, t *testing.T, projectDir string) *environment {
	t.Helper()

	workDir := t.TempDir()

	return &environment{
		t:              t,
		ctx:            ctx,
		projectDir:     projectDir,
		workDir:        workDir,
		kubeconfigPath: filepath.Join(workDir, "kubeconfig"),
		clusterName:    kindClusterName,
		netboxTokenRaw: netboxSuperuserToken,
		managerDone:    make(chan error, 1),
	}
}

func (e *environment) Setup() error {
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

func (e *environment) Cleanup() {
	if e.managerCmd != nil && e.managerCmd.Process != nil {
		_ = e.managerCmd.Process.Kill()
		select {
		case <-e.managerDone:
		case <-time.After(5 * time.Second):
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
	if e.clusterName != "" {
		_ = e.runCmd(e.ctx, e.projectDir, "kind", "delete", "cluster", "--name", e.clusterName)
	}
}

func (e *environment) NetBoxURL() string {
	return e.netboxURL
}

func (e *environment) NetBoxToken() string {
	return e.netboxToken
}

func (e *environment) startNetBox() error {
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
		testcontainers.WithWaitStrategyAndDeadline(10*time.Minute,
			wait.ForHTTP("/api/").
				WithPort("8080/tcp").
				WithAllowInsecure(true).
				WithStatusCodeMatcher(func(code int) bool {
					return code == http.StatusOK || code == http.StatusFound || code == http.StatusForbidden
				}).
				WithStartupTimeout(10*time.Minute),
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

func (e *environment) resolveNetBoxAPIToken(ctx context.Context) (string, error) {
	deadline := time.Now().Add(30 * time.Second)
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

func (e *environment) netboxAPITokenKey(ctx context.Context) (string, error) {
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

func (e *environment) createKindCluster(ctx context.Context) error {
	_ = e.runCmd(ctx, e.projectDir, "kind", "delete", "cluster", "--name", e.clusterName)
	return e.runCmd(
		ctx,
		e.projectDir,
		"kind",
		"create",
		"cluster",
		"--name",
		e.clusterName,
		"--kubeconfig",
		e.kubeconfigPath,
	)
}

func (e *environment) installCRDs(ctx context.Context) error {
	capiModuleDir, err := clusterAPIModuleDir()
	if err != nil {
		return err
	}

	capiCRDs := []string{
		filepath.Join(capiModuleDir, "config", "crd", "bases", "cluster.x-k8s.io_clusters.yaml"),
		filepath.Join(capiModuleDir, "config", "crd", "bases", "ipam.cluster.x-k8s.io_ipaddressclaims.yaml"),
		filepath.Join(capiModuleDir, "config", "crd", "bases", "ipam.cluster.x-k8s.io_ipaddresses.yaml"),
	}

	args := []string{"--kubeconfig", e.kubeconfigPath, "apply"}
	for _, crd := range capiCRDs {
		args = append(args, "-f", crd)
	}
	args = append(args, "-f", filepath.Join(e.projectDir, "config", "crd", "bases"))
	err = e.runCmd(ctx, e.projectDir, "kubectl", args...)
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
		err = e.runCmd(ctx, e.projectDir,
			"kubectl", "--kubeconfig", e.kubeconfigPath,
			"wait", "--for=condition=Established", "--timeout=2m",
			fmt.Sprintf("crd/%s", crdName),
		)
		if err != nil {
			return err
		}
	}

	return nil
}

func (e *environment) startManager(ctx context.Context) error {
	args := []string{
		"run", "./cmd/main.go",
		"--leader-elect=false",
		"--metrics-bind-address=0",
		"--health-probe-bind-address=0",
	}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = e.projectDir
	cmd.Env = append(os.Environ(),
		"KUBECONFIG="+e.kubeconfigPath,
		"GOCACHE="+filepath.Join(e.projectDir, ".cache", "go-build"),
	)
	cmd.Stdout = &e.managerOutput
	cmd.Stderr = &e.managerOutput

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start manager: %w", err)
	}
	e.managerCmd = cmd

	go func() {
		e.managerDone <- cmd.Wait()
	}()

	time.Sleep(managerStartupWait)
	select {
	case err := <-e.managerDone:
		return fmt.Errorf("manager exited early: %w\n%s", err, e.managerOutput.String())
	default:
	}

	return nil
}

func (e *environment) RunChainsawScenario(ctx context.Context, scenarioDir string, values map[string]string) error {
	valuesPath := filepath.Join(e.workDir, filepath.Base(scenarioDir)+"-values.yaml")
	if err := os.WriteFile(valuesPath, []byte(renderValues(values)), 0o644); err != nil {
		return fmt.Errorf("write chainsaw values: %w", err)
	}

	output, err := e.runCmdOutput(ctx, e.projectDir, []string{"KUBECONFIG=" + e.kubeconfigPath},
		"go",
		"tool", "chainsaw",
		"test",
		"--config", filepath.Join("test", "e2e", "chainsaw.yaml"),
		"--kube-context", kindContextName,
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
		if managerOutput := strings.TrimSpace(e.managerOutput.String()); managerOutput != "" {
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

func (e *environment) debugClusterState(ctx context.Context) string {
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
			e.projectDir,
			nil,
			"kubectl",
			append([]string{"--kubeconfig", e.kubeconfigPath}, args...)...)
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

func (e *environment) GetClaimUID(ctx context.Context, namespace, claimName string) (string, error) {
	output, err := e.runCmdOutput(ctx, e.projectDir, nil,
		"kubectl",
		"--kubeconfig", e.kubeconfigPath,
		"-n", namespace,
		"get", "ipaddressclaim", claimName,
		"-o", "jsonpath={.metadata.uid}",
	)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func (e *environment) CleanupScenario(ctx context.Context, scenario scenario) error {
	if err := e.runCmd(ctx, e.projectDir,
		"kubectl",
		"--kubeconfig", e.kubeconfigPath,
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
			"--kubeconfig", e.kubeconfigPath,
			"delete", resource.kind, resource.name,
			"--wait=true",
			"--timeout=2m",
			"--ignore-not-found=true",
		}
		if resource.namespace != "" {
			args = append(
				[]string{
					"--kubeconfig",
					e.kubeconfigPath,
					"-n",
					resource.namespace,
					"delete",
					resource.kind,
					resource.name,
				},
				args[4:]...)
		}
		if err := e.runCmd(ctx, e.projectDir, "kubectl", args...); err != nil {
			return err
		}
	}

	return nil
}

func (e *environment) DeleteClaim(ctx context.Context, namespace, name string) error {
	return e.runCmd(ctx, e.projectDir,
		"kubectl",
		"--kubeconfig", e.kubeconfigPath,
		"-n", namespace,
		"delete", "ipaddressclaims.ipam.cluster.x-k8s.io", name,
		"--wait=false",
		"--ignore-not-found=true",
	)
}

func (e *environment) waitForNamespaceDeleted(ctx context.Context, namespace string, timeout time.Duration) error {
	return e.waitForResourceDeleted(ctx, "", "namespace", namespace, timeout)
}

func (e *environment) waitForResourceDeleted(
	ctx context.Context,
	namespace, resource, name string,
	timeout time.Duration,
) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		args := []string{
			"--kubeconfig", e.kubeconfigPath,
		}
		if namespace != "" {
			args = append(args, "-n", namespace)
		}
		args = append(args,
			"get", resource, name,
			"--ignore-not-found",
			"-o", "name",
		)
		output, err := e.runCmdOutput(ctx, e.projectDir, nil, "kubectl", args...)
		if err != nil {
			return err
		}
		if strings.TrimSpace(output) == "" {
			return nil
		}
		time.Sleep(2 * time.Second)
	}

	scope := name
	if namespace != "" {
		scope = namespace + "/" + name
	}
	return fmt.Errorf("timed out waiting for %s %q deletion", resource, scope)
}

func (e *environment) AssertIPAddress(t *testing.T, ipAddress *netBoxIPAddress, scenario scenario) {
	t.Helper()

	if ipAddress.DNSName != scenario.expectedDNSName {
		t.Fatalf("unexpected dns name: got %q want %q", ipAddress.DNSName, scenario.expectedDNSName)
	}
	if ipAddress.Status == nil || ipAddress.Status.Value != scenario.expectedStatus {
		t.Fatalf("unexpected status: %#v", ipAddress.Status)
	}

	tags := make(map[string]struct{}, len(ipAddress.Tags))
	for _, tag := range ipAddress.Tags {
		tags[tag.Name] = struct{}{}
	}
	for _, expectedTag := range scenario.expectedTags {
		if _, ok := tags[expectedTag]; !ok {
			t.Fatalf("expected tag %q in %#v", expectedTag, ipAddress.Tags)
		}
	}

	for key, expectedValue := range scenario.expectedCustomFields {
		if got := fmt.Sprint(ipAddress.CustomFields[key]); got != expectedValue {
			t.Fatalf("unexpected custom field %q: got %q want %q", key, got, expectedValue)
		}
	}
}

func (e *environment) failureDetails(ctx context.Context) string {
	var details strings.Builder
	if managerOutput := strings.TrimSpace(e.managerOutput.String()); managerOutput != "" {
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

func (e *environment) debugNetBoxState(ctx context.Context) string {
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

func (e *environment) CreatePrefix(ctx context.Context, cidr string) (*netBoxPrefix, error) {
	return e.netboxClient.createPrefix(ctx, cidr)
}

//nolint:nilnil // nil, nil is this test helper's "not found" result; callers check the pointer.
func (e *environment) FindIPAddressByClaimUID(
	ctx context.Context,
	ownershipTag, fieldName, claimUID string,
) (*netBoxIPAddress, error) {
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

func (e *environment) WaitForIPAddressDeleted(
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
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out waiting for NetBox IP cleanup for claim UID %q", claimUID)
}

func (e *environment) ensureCustomField(ctx context.Context, fieldName string) error {
	return e.netboxClient.ensureCustomField(ctx, fieldName)
}

func (e *environment) runCmd(ctx context.Context, dir string, name string, args ...string) error {
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

func (e *environment) runCmdOutput(
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
		httpClient: &http.Client{Timeout: 30 * time.Second},
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

func resolveProjectDir(start string) (string, error) {
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

func (c *netBoxAdminClient) createPrefix(ctx context.Context, cidr string) (*netBoxPrefix, error) {
	request := map[string]any{
		"prefix": cidr,
		"status": "active",
	}
	var prefix netBoxPrefix
	if err := c.do(ctx, http.MethodPost, "/api/ipam/prefixes/", nil, request, &prefix, http.StatusCreated); err != nil {
		return nil, fmt.Errorf("create prefix %q: %w", cidr, err)
	}
	return &prefix, nil
}

func (c *netBoxAdminClient) listIPAddresses(
	ctx context.Context,
	query url.Values,
) (*netBoxListResponse[netBoxIPAddress], error) {
	var result netBoxListResponse[netBoxIPAddress]
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
	httpReq.Header.Set("User-Agent", nb.UserAgent)
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

var _ = template.Must

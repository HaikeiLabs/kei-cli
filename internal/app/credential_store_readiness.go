package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// Credential store readiness (HAI-1023) is one answer, from the catalog, to
// "can a secret be delivered in this scope?":
//
//	GET /api/v1/organizations/{org}/workspaces/{ws}/credential-store/readiness[?agent_id=]
//
// kei credential-store status prints it, and every secret path (connector
// create and reconnect, model profile create and update) checks it first so
// they all fail with the same message for the same cause.

// Readiness reasons, as the catalog reports them.
const (
	readinessNoStore       = "no_store"
	readinessStoreDisabled = "store_disabled"
	readinessNoSyncRuntime = "no_sync_runtime"
)

// credentialStorePutCommand is the exact command that creates the
// organization's credential store. The CLI prints it and never creates a
// store on its own, so admin scripts stay predictable.
const credentialStorePutCommand = `kei credential-store put --secret-backend aws-secrets-manager --backend-config '{"region":"REGION"}' --secret-name-prefix PREFIX`

// credentialStorePutBackends names the other backends' --backend-config.
const credentialStorePutBackends = `(for azure-key-vault use --backend-config '{"key_vault_uri":"URI"}'; for gcp-secret-manager use --backend-config '{"project_id":"PROJECT"}')`

// Next steps when the catalog sends none.
const (
	storeDisabledFallbackStep = "enable the credential store with kei credential-store update --status active"
	syncRuntimeFallbackStep   = "register a credential-sync runtime in this scope: run kei-proxy credential sync on a runtime in this workspace"
)

// credentialStoreReadiness is the readiness response. It carries no key
// material; decoding into this type drops any field the contract does not
// name.
type credentialStoreReadiness struct {
	Store        *readinessStore `json:"store"`
	SyncRuntimes int             `json:"sync_runtimes"`
	Ready        bool            `json:"ready"`
	Reasons      []string        `json:"reasons"`
	NextStep     string          `json:"next_step"`
}

type readinessStore struct {
	ID            string `json:"id"`
	Scope         string `json:"scope"`
	SecretBackend string `json:"secret_backend"`
	Status        string `json:"status"`
}

// hasReason reports whether the catalog gave reason.
func (r credentialStoreReadiness) hasReason(reason string) bool {
	for _, got := range r.Reasons {
		if got == reason {
			return true
		}
	}
	return false
}

// nextStep is the one thing to do next. With no store it is always the put
// command; otherwise the catalog's next step, or the CLI's own when the
// catalog sends none.
func (r credentialStoreReadiness) nextStep() string {
	switch {
	case r.hasReason(readinessNoStore):
		return "create the organization's credential store with: " + credentialStorePutCommand + " " + credentialStorePutBackends
	case r.NextStep != "":
		return r.NextStep
	case r.hasReason(readinessStoreDisabled):
		return storeDisabledFallbackStep
	case r.hasReason(readinessNoSyncRuntime):
		return syncRuntimeFallbackStep
	default:
		return ""
	}
}

// credentialStoreReadinessPath is the readiness singleton of a workspace.
func credentialStoreReadinessPath(orgID, workspaceID string) string {
	return "/api/v1/organizations/" + url.PathEscape(orgID) + "/workspaces/" + url.PathEscape(workspaceID) + "/credential-store/readiness"
}

func readinessQuery(agentID string) url.Values {
	if agentID == "" {
		return nil
	}
	return url.Values{"agent_id": {agentID}}
}

// errReadinessUnavailable means the catalog (or the console in front of it)
// has no readiness route yet; callers fall back to the recipients check.
var errReadinessUnavailable = errors.New("credential store readiness is not available")

// readinessUnavailable reports whether a failed readiness call means an
// older catalog or console rather than a refusal: 404, 405, or no HTTP
// answer from an API at all (the console's HTML fallthrough). A transport
// error lands here too; the recipients check that follows reports it.
func readinessUnavailable(status int) bool {
	return status == 0 || status == http.StatusNotFound || status == http.StatusMethodNotAllowed
}

func decodeReadiness(payload []byte) (credentialStoreReadiness, error) {
	var readiness credentialStoreReadiness
	if err := json.Unmarshal(payload, &readiness); err != nil {
		return credentialStoreReadiness{}, fmt.Errorf("decode credential store readiness: %w", err)
	}
	return readiness, nil
}

// credentialStoreNotReadyError is the one message every secret path prints
// when a scope is not ready.
type credentialStoreNotReadyError struct {
	readiness credentialStoreReadiness
}

func (e *credentialStoreNotReadyError) Error() string {
	reasons := strings.Join(e.readiness.Reasons, ", ")
	if reasons == "" {
		reasons = "not ready"
	}
	return "credential store not ready (" + reasons + "): " + e.readiness.nextStep()
}

// requireReadiness turns a readiness answer into the shared error. A
// credential-ref connector (needSyncRuntime false) needs only an active
// store: its secret already lives in the customer's secret manager.
func requireReadiness(readiness credentialStoreReadiness, needSyncRuntime bool) error {
	if readiness.Ready {
		return nil
	}
	if needSyncRuntime {
		return &credentialStoreNotReadyError{readiness: readiness}
	}
	storeOnly := readiness
	storeOnly.Reasons = nil
	for _, reason := range readiness.Reasons {
		if reason != readinessNoSyncRuntime {
			storeOnly.Reasons = append(storeOnly.Reasons, reason)
		}
	}
	if len(storeOnly.Reasons) == 0 {
		return nil
	}
	if len(storeOnly.Reasons) != len(readiness.Reasons) {
		// The catalog's next step may be about the sync runtime this
		// path does not need; use the CLI's store step instead.
		storeOnly.NextStep = ""
	}
	return &credentialStoreNotReadyError{readiness: storeOnly}
}

// recipientsNotReady maps a recipients answer to the same error, for a
// catalog without the readiness route.
func recipientsNotReady(recipients credentialRecipients) error {
	switch {
	case recipients.CredentialStoreInstallationID == "":
		return &credentialStoreNotReadyError{readiness: credentialStoreReadiness{Reasons: []string{readinessNoStore}}}
	case len(recipients.Recipients) == 0:
		return &credentialStoreNotReadyError{readiness: credentialStoreReadiness{Reasons: []string{readinessNoSyncRuntime}}}
	default:
		return nil
	}
}

// fetchReadiness reads readiness through the connector session. It returns
// errReadinessUnavailable for an older catalog.
func (s *connectorSession) fetchReadiness() (credentialStoreReadiness, error) {
	payload, err := s.doPath(http.MethodGet, credentialStoreReadinessPath(s.orgID, s.workspaceID), nil)
	if err != nil {
		var statusErr *connectorStatusError
		status := 0
		if errors.As(err, &statusErr) {
			status = statusErr.StatusCode
		}
		if readinessUnavailable(status) {
			return credentialStoreReadiness{}, errReadinessUnavailable
		}
		return credentialStoreReadiness{}, fmt.Errorf("check credential store readiness: %w", err)
	}
	return decodeReadiness(payload)
}

// preflightCredentialStore checks the workspace before any secret or
// credential-ref write. It returns nil when the catalog has no readiness
// route, so the recipients check decides.
func (s *connectorSession) preflightCredentialStore(needSyncRuntime bool) error {
	readiness, err := s.fetchReadiness()
	if errors.Is(err, errReadinessUnavailable) {
		return nil
	}
	if err != nil {
		return err
	}
	return requireReadiness(readiness, needSyncRuntime)
}

// fetchReadiness reads readiness for the session's workspace and an optional
// agent. It returns errReadinessUnavailable for an older catalog.
func (s *orgSession) fetchReadiness(agentID string) (credentialStoreReadiness, error) {
	status, payload, err := s.do(http.MethodGet, credentialStoreReadinessPath(s.orgID, s.workspaceID), readinessQuery(agentID), nil)
	if err != nil {
		if readinessUnavailable(status) {
			return credentialStoreReadiness{}, errReadinessUnavailable
		}
		return credentialStoreReadiness{}, fmt.Errorf("check credential store readiness: %w", err)
	}
	return decodeReadiness(payload)
}

// preflightCredentialStore checks a model profile's scope before its API key
// is sealed. An organization-level profile has no workspace to check, so the
// recipients check decides.
func (s *orgSession) preflightCredentialStore(agentID string) error {
	if s.workspaceID == "" {
		return nil
	}
	readiness, err := s.fetchReadiness(agentID)
	if errors.Is(err, errReadinessUnavailable) {
		return nil
	}
	if err != nil {
		return err
	}
	return requireReadiness(readiness, true)
}

// runCredentialStoreStatus prints the readiness of a workspace, or of an
// agent in it: the resolved store, the credential-sync runtime count, whether
// a secret can be delivered, and the next step.
func runCredentialStoreStatus(args []string, stdout, stderr io.Writer, client *http.Client, store credentialStore) int {
	lc := newCommandFlags("credential-store", "status", "Show whether secrets can be delivered in a workspace", stdout, stderr)
	workspace := workspaceFlagHelp(lc.fs, "workspace name (default: the selected or default workspace)")
	agentID := lc.fs.String("agent", "", "agent ID: check the runtimes that serve this agent")
	jsonOutput := lc.fs.Bool("json", false, "print the readiness as JSON")
	rc, ok := parseLeaf(lc, args)
	if !ok {
		return rc
	}
	if lc.fs.NArg() != 0 {
		fmt.Fprintln(stderr, "credential-store status takes no positional arguments")
		return 2
	}
	if *agentID != "" {
		if _, err := uuid.Parse(*agentID); err != nil {
			fmt.Fprintln(stderr, "credential-store status: --agent must be an agent ID (UUID)")
			return 2
		}
	}
	session, ok := openOrgSession("credential-store status", workspaceSelection{}, stderr, client, store)
	if !ok {
		return 1
	}
	sel, err := selectWorkspace(*workspace, session.baseURL)
	if err == nil {
		session.workspaceID, err = resolveWorkspaceSelection(context.Background(), client, session.baseURL, session.token, sel)
	}
	if err != nil {
		fmt.Fprintf(stderr, "credential-store status: %v\n", err)
		return 1
	}
	readiness, err := session.fetchReadiness(*agentID)
	if errors.Is(err, errReadinessUnavailable) {
		fmt.Fprintln(stderr, "credential-store status: the control plane does not serve credential store readiness yet; it needs the HAI-1023 catalog update")
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "credential-store status: %v\n", err)
		return 1
	}
	if *jsonOutput {
		encoded, err := json.Marshal(readiness)
		if err != nil {
			fmt.Fprintf(stderr, "credential-store status: encode readiness: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintln(stdout, string(encoded))
		return 0
	}
	writeReadiness(stdout, session.workspaceID, *agentID, readiness)
	return 0
}

func writeReadiness(w io.Writer, workspaceID, agentID string, r credentialStoreReadiness) {
	fmt.Fprintf(w, "Workspace:      %s\n", workspaceID)
	if agentID != "" {
		fmt.Fprintf(w, "Agent:          %s\n", agentID)
	}
	if r.Store == nil {
		fmt.Fprintln(w, "Store:          none")
	} else {
		fmt.Fprintf(w, "Store:          %s (%s, %s, %s)\n", r.Store.ID, r.Store.Scope, r.Store.SecretBackend, r.Store.Status)
	}
	fmt.Fprintf(w, "Sync runtimes:  %d\n", r.SyncRuntimes)
	if r.Ready {
		fmt.Fprintln(w, "Ready:          yes")
	} else {
		fmt.Fprintf(w, "Ready:          no (%s)\n", strings.Join(r.Reasons, ", "))
	}
	if step := r.nextStep(); step != "" {
		fmt.Fprintf(w, "Next step:      %s\n", step)
	}
}

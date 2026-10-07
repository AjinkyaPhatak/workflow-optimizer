package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"workflow-optimizer/internal/credential"
	"workflow-optimizer/internal/node"
)

// Credential error codes (never retryable). They are the codes the LLM node
// reports for the same failures.
const (
	ErrCodeCredentialNotFound         node.ErrorCode = "CREDENTIAL_NOT_FOUND"
	ErrCodeCredentialDecryptionFailed node.ErrorCode = "CREDENTIAL_DECRYPTION_FAILED"
	ErrCodeCredentialProviderMismatch node.ErrorCode = "CREDENTIAL_PROVIDER_MISMATCH"
	ErrCodeCredentialInvalid          node.ErrorCode = "CREDENTIAL_INVALID"
	// ErrCodeCredentialRevoked: the connected account must be connected
	// again (disconnected, revoked, expired without refresh). Phase C2.
	ErrCodeCredentialRevoked node.ErrorCode = "CREDENTIAL_REVOKED"
)

// Connector is the generic boundary in front of a provider-specific client:
// it turns a resolved credential into a client of type C (a GmailClient, a
// DiscordClient...). Connect must not perform network I/O it cannot cancel
// through ctx, and the client must not outlive the action call: the
// credential it holds is runtime-only. For an integration without auth the
// credential is the zero value.
type Connector[C any] interface {
	Connect(ctx context.Context, cred credential.ResolvedCredential) (C, error)
}

// ConnectorFunc adapts a function to Connector.
type ConnectorFunc[C any] func(ctx context.Context, cred credential.ResolvedCredential) (C, error)

func (f ConnectorFunc[C]) Connect(ctx context.Context, cred credential.ResolvedCredential) (C, error) {
	return f(ctx, cred)
}

// Handler performs one action with a connected client. It reads its inputs
// from in (ports and already-resolved config) and returns port outputs. It
// returns *Error (or plain errors, normalized by ToNodeError); it never
// retries.
type Handler[C any] func(ctx context.Context, client C, in node.NodeInput) (node.NodeOutput, error)

// ActionNode is the executable node of one integration action:
//
//	config.credential_id -> credential.Resolver (workspace + provider checked)
//	    -> ResolvedCredential -> Connector -> client -> Handler -> NodeOutput
//
// It is an ordinary node.Node: it is stateless, respects ctx, does not
// retry, and touches neither PostgreSQL, Redis nor execution state. The
// resolved credential lives only for the duration of Execute.
type ActionNode[C any] struct {
	nodeType    string
	integration string
	action      string
	auth        Auth
	creds       credential.Resolver
	connector   Connector[C]
	handler     Handler[C]
}

// NewActionNode builds the node of action actionID of in. creds may be nil
// only when the integration needs no auth.
func NewActionNode[C any](in Integration, actionID string, creds credential.Resolver, connector Connector[C], handler Handler[C]) (*ActionNode[C], error) {
	if _, ok := in.Action(actionID); !ok {
		return nil, fmt.Errorf("%w: %q has no action %q", ErrInvalid, in.ID, actionID)
	}
	if connector == nil || handler == nil {
		return nil, fmt.Errorf("%w: action %q needs a connector and a handler", ErrInvalid, NodeType(in.ID, actionID))
	}
	return &ActionNode[C]{
		nodeType: NodeType(in.ID, actionID), integration: in.ID, action: actionID,
		auth: in.Auth, creds: creds, connector: connector, handler: handler,
	}, nil
}

// Type implements node.Node.
func (n *ActionNode[C]) Type() string { return n.nodeType }

// Execute implements node.Node.
func (n *ActionNode[C]) Execute(ctx context.Context, in node.NodeInput) (node.NodeOutput, error) {
	if err := ctx.Err(); err != nil {
		return node.NodeOutput{}, err
	}
	for k := range in.Config {
		if IsSecretConfigKey(k) {
			return node.NodeOutput{}, node.NewNodeError(node.ErrCodeConfiguration,
				fmt.Sprintf("config must not contain %q: reference a workspace credential with %s", k, node.CredentialConfigField), false)
		}
	}
	var cred credential.ResolvedCredential
	if n.auth.Required {
		resolved, err := n.resolve(ctx, in)
		if err != nil {
			return node.NodeOutput{}, err
		}
		cred = resolved
	}
	client, err := n.connector.Connect(ctx, cred)
	cred = credential.ResolvedCredential{} // only the client holds it now
	if err != nil {
		return node.NodeOutput{}, ToNodeError(ctx, n.integration, n.action, err)
	}
	out, err := n.handler(ctx, client, in)
	if err != nil {
		return node.NodeOutput{}, ToNodeError(ctx, n.integration, n.action, err)
	}
	return out, nil
}

func (n *ActionNode[C]) resolve(ctx context.Context, in node.NodeInput) (credential.ResolvedCredential, error) {
	raw, _ := in.GetStringConfig(node.CredentialConfigField)
	if strings.TrimSpace(raw) == "" {
		return credential.ResolvedCredential{}, node.NewNodeError(ErrCodeCredentialInvalid, "an account is required: set "+node.CredentialConfigField, false)
	}
	id, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return credential.ResolvedCredential{}, node.NewNodeError(ErrCodeCredentialInvalid, node.CredentialConfigField+" is not a valid identifier", false)
	}
	if n.creds == nil {
		return credential.ResolvedCredential{}, node.NewNodeError(ErrCodeCredentialInvalid, "credential resolution is not configured", false)
	}
	resolved, err := n.creds.Resolve(ctx, in.Scope.WorkspaceID, id, n.auth.Provider)
	if err != nil {
		return credential.ResolvedCredential{}, ClassifyCredentialError(err)
	}
	if resolved.Type != n.auth.CredentialType {
		return credential.ResolvedCredential{}, node.NewNodeError(ErrCodeCredentialInvalid,
			fmt.Sprintf("the credential is a %s credential; %s needs %s", resolved.Type, n.nodeType, n.auth.CredentialType), false)
	}
	return resolved, nil
}

// ClassifyCredentialError maps credential failures to their non-retryable
// codes; anything else (e.g. the credential store being unreachable) is a
// transient failure. Cancellation passes through unchanged.
func ClassifyCredentialError(err error) error {
	var code node.ErrorCode
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, credential.ErrNotFound):
		code = ErrCodeCredentialNotFound
	case errors.Is(err, credential.ErrDecryptionFailed):
		code = ErrCodeCredentialDecryptionFailed
	case errors.Is(err, credential.ErrProviderMismatch):
		code = ErrCodeCredentialProviderMismatch
	case errors.Is(err, credential.ErrInvalid):
		code = ErrCodeCredentialInvalid
	case errors.Is(err, credential.ErrRevoked):
		code = ErrCodeCredentialRevoked
	default:
		return node.WrapNodeError(node.ErrCodeUnavailable, "credential could not be resolved", true, err)
	}
	return node.WrapNodeError(code, "credential could not be used", false, err)
}

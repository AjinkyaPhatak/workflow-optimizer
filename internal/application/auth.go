package application

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"workflow-optimizer/internal/auth"
	"workflow-optimizer/internal/workspace"
)

// IdentityStore persists users and their workspaces.
type IdentityStore interface {
	auth.UserRepository
	MembershipStore
	CreateUserWithWorkspace(ctx context.Context, u auth.User, workspaceName string) (auth.User, workspace.Workspace, error)
	ListMemberships(ctx context.Context, userID uuid.UUID) ([]workspace.Membership, error)
}

// AuthService registers users and issues tokens.
type AuthService struct {
	identities IdentityStore
	tokens     auth.TokenService
}

// NewAuthService wires the identity store and the token service.
func NewAuthService(identities IdentityStore, tokens auth.TokenService) *AuthService {
	return &AuthService{identities: identities, tokens: tokens}
}

// Session is an issued bearer token.
type Session struct {
	Token  string
	Claims auth.Claims
	User   auth.User
}

// RegisterInput is a new account.
type RegisterInput struct {
	Email         string
	Name          string
	Password      string
	WorkspaceName string
}

// NormalizeEmail is the stored form of an email address.
func NormalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// Register creates the user with a workspace they own and signs them in.
func (s *AuthService) Register(ctx context.Context, in RegisterInput) (Session, workspace.Workspace, error) {
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return Session{}, workspace.Workspace{}, &InvalidError{Message: "password must be 8 to 72 bytes"}
	}
	wsName := strings.TrimSpace(in.WorkspaceName)
	if wsName == "" {
		wsName = strings.TrimSpace(in.Name) + "'s workspace"
	}
	u, ws, err := s.identities.CreateUserWithWorkspace(ctx, auth.User{
		Email: NormalizeEmail(in.Email), Name: strings.TrimSpace(in.Name), PasswordHash: hash,
	}, wsName)
	if errors.Is(err, auth.ErrEmailTaken) {
		return Session{}, workspace.Workspace{}, &ConflictError{Code: "EMAIL_TAKEN", Message: "An account with this email already exists"}
	}
	if err != nil {
		return Session{}, workspace.Workspace{}, err
	}
	sess, err := s.issue(ctx, u)
	return sess, ws, err
}

// Login checks the password and issues a token. Unknown emails and wrong
// passwords fail identically (ErrInvalidCredentials) and take as long.
func (s *AuthService) Login(ctx context.Context, email, password string) (Session, error) {
	u, err := s.identities.FindByEmail(ctx, NormalizeEmail(email))
	if errors.Is(err, auth.ErrUserNotFound) {
		auth.CheckPasswordAgainstNothing(password)
		return Session{}, ErrInvalidCredentials
	}
	if err != nil {
		return Session{}, err
	}
	if !auth.CheckPassword(u.PasswordHash, password) {
		return Session{}, ErrInvalidCredentials
	}
	return s.issue(ctx, u)
}

func (s *AuthService) issue(ctx context.Context, u auth.User) (Session, error) {
	token, claims, err := s.tokens.Generate(ctx, u)
	if err != nil {
		return Session{}, err
	}
	return Session{Token: token, Claims: claims, User: u}, nil
}

// Me returns the user and their workspaces.
func (s *AuthService) Me(ctx context.Context, user uuid.UUID) (auth.User, []workspace.Membership, error) {
	u, err := s.identities.FindByID(ctx, user)
	if errors.Is(err, auth.ErrUserNotFound) {
		return auth.User{}, nil, notFound("user")
	}
	if err != nil {
		return auth.User{}, nil, err
	}
	ms, err := s.identities.ListMemberships(ctx, user)
	return u, ms, err
}

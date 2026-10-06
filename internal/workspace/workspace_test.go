package workspace_test

import (
	"testing"

	"workflow-optimizer/internal/workspace"
)

func TestRolePermissions(t *testing.T) {
	all := []workspace.Action{workspace.ActionRead, workspace.ActionWrite, workspace.ActionDelete, workspace.ActionPublish,
		workspace.ActionExecute, workspace.ActionManageCredentials, workspace.ActionManageProjects}
	allowed := map[workspace.Role][]workspace.Action{
		workspace.RoleOwner:  all,
		workspace.RoleAdmin:  all,
		workspace.RoleMember: {workspace.ActionRead, workspace.ActionWrite, workspace.ActionExecute},
		workspace.RoleViewer: {workspace.ActionRead},
		"unknown":            nil,
	}
	for role, actions := range allowed {
		want := map[workspace.Action]bool{}
		for _, a := range actions {
			want[a] = true
		}
		for _, a := range all {
			if got := role.Can(a); got != want[a] {
				t.Errorf("%s can %s = %v, want %v", role, a, got, want[a])
			}
		}
	}
}

package workspaceuser

import (
	"fmt"

	workspacev1alpha1 "soradev.io/cluster-agent/api/v1alpha1"
)

func ServiceAccountName(wsConfig *workspacev1alpha1.WorkspaceUser) string {
	return fmt.Sprintf("%s-service-account", wsConfig.Spec.Username)
}

func UserRoleName(wsConfig *workspacev1alpha1.WorkspaceUser) string {
	return fmt.Sprintf("%s-role", wsConfig.Name)
}

func UserRoleBindingname(wsConfig *workspacev1alpha1.WorkspaceUser) string {
	return fmt.Sprintf("%s-rolebinding", UserRoleName(wsConfig))
}

func ServiceAccountSecretName(wsConfig *workspacev1alpha1.WorkspaceUser) string {
	return fmt.Sprintf("%s-secret", ServiceAccountName(wsConfig))
}

func BusyBoxPVCName() string {
	return "busybox-pvc"
}

func UserConfigNamespace(wsConfig *workspacev1alpha1.WorkspaceUser) string {
	return fmt.Sprintf("%s-config", wsConfig.Name)
}

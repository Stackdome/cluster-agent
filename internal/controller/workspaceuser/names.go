package workspaceuser

import (
	"fmt"

	userv1alpha1 "stackdome.io/cluster-agent/api/users/v1alpha1"
)

func ServiceAccountName(wsConfig *userv1alpha1.WorkspaceUser) string {
	return fmt.Sprintf("%s-service-account", wsConfig.Spec.Username)
}

func UserRoleName(wsConfig *userv1alpha1.WorkspaceUser) string {
	return fmt.Sprintf("%s-role", wsConfig.Name)
}

func UserRoleBindingname(wsConfig *userv1alpha1.WorkspaceUser) string {
	return fmt.Sprintf("%s-rolebinding", UserRoleName(wsConfig))
}

func ServiceAccountSecretName(wsConfig *userv1alpha1.WorkspaceUser) string {
	return fmt.Sprintf("%s-secret", ServiceAccountName(wsConfig))
}

func BusyBoxPVCName() string {
	return "busybox-pvc"
}

func UserConfigNamespace(wsConfig *userv1alpha1.WorkspaceUser) string {
	return fmt.Sprintf("%s-config", wsConfig.Name)
}

package workspaceconfiguration

import (
	"fmt"

	workspacev1alpha1 "soradev.io/cluster-agent/api/v1alpha1"
)

func ServiceAccountName(wsConfig *workspacev1alpha1.WorkspaceConfiguration) string {
	return fmt.Sprintf("%s-service-account", wsConfig.Spec.Username)
}

func UserRoleName(wsConfig *workspacev1alpha1.WorkspaceConfiguration) string {
	return fmt.Sprintf("%s-role", wsConfig.Spec.Username)
}

func UserRoleBindingname(wsConfig *workspacev1alpha1.WorkspaceConfiguration) string {
	return fmt.Sprintf("%s-rolebinding", UserRoleName(wsConfig))
}

func ServiceAccountSecretName(wsConfig *workspacev1alpha1.WorkspaceConfiguration) string {
	return fmt.Sprintf("%s-secret", ServiceAccountName(wsConfig))
}

//go:build (validation || infra.any || cluster.any || sanity) && !stress && !extended

package rbac

import (
	"strings"
	"testing"

	"github.com/rancher/shepherd/clients/rancher"
	management "github.com/rancher/shepherd/clients/rancher/generated/management/v3"
	"github.com/rancher/shepherd/extensions/clusters"
	"github.com/rancher/shepherd/extensions/rbac"
	"github.com/rancher/shepherd/extensions/users"
	"github.com/rancher/shepherd/pkg/config"
	"github.com/rancher/shepherd/pkg/session"
	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type RBTestSuite struct {
	suite.Suite
	client  *rancher.Client
	session *session.Session
	cluster *management.Cluster
}

func (rb *RBTestSuite) TearDownSuite() {
	rb.session.Cleanup()
}

func (rb *RBTestSuite) SetupSuite() {
	testSession := session.NewSession()
	rb.session = testSession

	client, err := rancher.NewClient("", testSession)
	require.NoError(rb.T(), err)

	rb.client = client

	log.Info("Getting cluster name from the config file and append cluster details in rb")
	clusterName := client.RancherConfig.ClusterName
	require.NotEmptyf(rb.T(), clusterName, "Cluster name to install should be set")
	clusterID, err := clusters.GetClusterIDByName(rb.client, clusterName)
	require.NoError(rb.T(), err, "Error getting cluster ID")
	rb.cluster, err = rb.client.Management.Cluster.ByID(clusterID)
	require.NoError(rb.T(), err)

}

func (rb *RBTestSuite) sequentialTestRBAC(role, member string, user *management.User) {
	standardClient, err := rb.client.AsUser(user)
	require.NoError(rb.T(), err)

	adminProject, err := createProject(rb.client, rb.cluster.ID)
	require.NoError(rb.T(), err)

	if member == standardUser {
		if strings.Contains(role, "project") {
			err := users.AddProjectMember(rb.client, adminProject, user, role, nil)
			require.NoError(rb.T(), err)
		} else {
			err := users.AddClusterRoleToUser(rb.client, rb.cluster, user, role, nil)
			require.NoError(rb.T(), err)
		}
	}

	standardClient, err = standardClient.ReLogin()
	require.NoError(rb.T(), err)

	additionalUser, err := users.CreateUserWithRole(rb.client, users.UserConfig(), standardUser)
	require.NoError(rb.T(), err)

	rb.Run("Validating Global Role Binding is created for "+role, func() {
		rbac.VerifyGlobalRoleBindingsForUser(rb.T(), user, rb.client)
	})
	rb.Run("Validating if "+role+" can list any downstream clusters", func() {
		rbac.VerifyUserCanListCluster(rb.T(), rb.client, standardClient, rb.cluster.ID, role)
	})
	rb.Run("Validating if members with role "+role+" are able to list all projects", func() {
		rbac.VerifyUserCanListProject(rb.T(), rb.client, standardClient, rb.cluster.ID, role, adminProject.Name)
	})
	rb.Run("Validating if members with role "+role+" is able to create a project in the cluster", func() {
		rbac.VerifyUserCanCreateProjects(rb.T(), rb.client, standardClient, rb.cluster.ID, role)
	})
	rb.Run("Validate namespaces checks for members with role "+role, func() {
		rbac.VerifyUserCanCreateNamespace(rb.T(), rb.client, standardClient, adminProject, rb.cluster.ID, role)
	})
	rb.Run("Validating if "+role+" can lists all namespaces in a cluster.", func() {
		rbac.VerifyUserCanListNamespace(rb.T(), rb.client, standardClient, adminProject, rb.cluster.ID, role)
	})
	rb.Run("Validating if "+role+" can delete a namespace from a project they own.", func() {
		rbac.VerifyUserCanDeleteNamespace(rb.T(), rb.client, standardClient, adminProject, rb.cluster.ID, role)
	})
	rb.Run("Validating if member with role "+role+" can add members to the cluster", func() {
		rbac.VerifyUserCanAddClusterRoles(rb.T(), rb.client, standardClient, rb.cluster, role)
	})
	rb.Run("Validating if member with role "+role+" can add members to the project", func() {
		if strings.Contains(role, "project") {
			rbac.VerifyUserCanAddProjectRoles(rb.T(), standardClient, adminProject, additionalUser, roleProjectOwner, rb.cluster.ID, role)
		}
	})
	rb.Run("Validating if member with role "+role+" can delete a project they are not owner of ", func() {
		rbac.VerifyUserCanDeleteProject(rb.T(), standardClient, adminProject, role)
	})
	rb.Run("Validating if member with role "+role+" is removed from the cluster and returns nil clusters", func() {
		if strings.Contains(role, "cluster") {
			rbac.VerifyUserCanRemoveClusterRoles(rb.T(), rb.client, user)
		}
	})
}

func (rb *RBTestSuite) TestRBAC() {
	tests := []struct {
		name   string
		role   string
		member string
	}{
		{"Cluster Owner", roleOwner, standardUser},
		{"Cluster Member", roleMember, standardUser},
		{"Project Owner", roleProjectOwner, standardUser},
		{"Project Member", roleProjectMember, standardUser},
		{"Restricted Admin", restrictedAdmin, restrictedAdmin},
	}

	for _, tt := range tests {
		var newUser *management.User
		rb.Run("Validate conditions for user with role "+tt.name, func() {
			user, err := users.CreateUserWithRole(rb.client, users.UserConfig(), tt.member)
			require.NoError(rb.T(), err)
			newUser = user
			rb.T().Logf("Created user: %v", newUser.Username)
		})

		if newUser != nil {
			rb.sequentialTestRBAC(tt.role, tt.member, newUser)
			subSession := rb.session.NewSession()
			defer subSession.Cleanup()
		}
	}
}

func (rb *RBTestSuite) TestRBACDynamicInput() {
	roles := map[string]string{
		"cluster-owner":    roleOwner,
		"cluster-member":   roleMember,
		"project-owner":    roleProjectOwner,
		"project-member":   roleProjectMember,
		"restricted-admin": restrictedAdmin,
	}
	var member string
	userConfig := new(rbac.Config)
	config.LoadConfig(rbac.ConfigurationFileKey, userConfig)
	username := userConfig.Username
	userByName, err := users.GetUserIDByName(rb.client, username)
	require.NoError(rb.T(), err)
	user, err := rb.client.Management.User.ByID(userByName)
	require.NoError(rb.T(), err)

	user.Password = userConfig.Password

	role := userConfig.Role
	if userConfig.Role == "" {
		rb.T().Skip()
	}else {
		val, ok := roles[role]
		if !ok {
			rb.FailNow("Incorrect usage of roles. Please go through the readme for correct role configurations")
		}
		role = val
	}

	if role == restrictedAdmin {
		member = restrictedAdmin
	} else {
		member = standardUser
	}
	rb.sequentialTestRBAC(role, member, user)

}

func TestRBACTestSuite(t *testing.T) {
	suite.Run(t, new(RBTestSuite))
}

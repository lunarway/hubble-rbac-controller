package iam

import (
	"fmt"
	"github.com/aws/aws-sdk-go/service/iam"
	iamCore "github.com/lunarway/hubble-rbac-controller/internal/core/iam"
	log "github.com/sirupsen/logrus"
	"strings"
)

type ApplyEventType int

const (
	RoleUpdated ApplyEventType = iota
	RoleCreated
	RoleDeleted
	PolicyUpdated
	PolicyCreated
	PolicyDeleted
)

type ApplyEventLister interface {
	handle(eventType ApplyEventType, name string)
}

type Applier struct {
	accountId string
	region string
	client *Client
	eventListener ApplyEventLister
}

func NewApplier(client *Client, accountId string, region string, eventListener ApplyEventLister) *Applier {
	return &Applier{
		accountId: accountId,
		region: region,
		client:client,
		eventListener:eventListener,
	}
}

//TODO: replace all this Sprintf'ing with go templating!
func (applier *Applier) buildDatabaseLoginPolicyDocument(policy *iamCore.DatabaseLoginPolicy) string {

	var statements []string

	for _,database := range policy.Databases {

		dbUserTemplate := "arn:aws:redshift:%s:%s:dbuser:%s/%s"
		dbNameTemplate := "arn:aws:redshift:%s:%s:dbname:%s/%s"

		dbUser := fmt.Sprintf(dbUserTemplate, applier.region, applier.accountId, database.ClusterIdentifier, policy.DatabaseUsername)
		dbName := fmt.Sprintf(dbNameTemplate, applier.region, applier.accountId, database.ClusterIdentifier, database.Name)

		statementTemplate := `
	     {
	         "Effect": "Allow",
	         "Action": "redshift:GetClusterCredentials",
	         "Resource": [
	             "%s",
	             "%s"
	         ],
	         "Condition": {
	             "StringLike": {
	                 "aws:userid": "*:%s"
	             }
	         }
	     }
`
		statement := fmt.Sprintf(statementTemplate, dbUser, dbName, policy.Email)
		statements = append(statements, statement)
	}

		documentTemplate := `
	{
	 "Version": "2012-10-17",
	 "Statement": [
	     %s
	 ]
	}
`
	document := fmt.Sprintf(documentTemplate, strings.Join(statements, ","))

	return strings.TrimSpace(document)
}

func (applier *Applier) lookupRole(roles []*iam.Role, name string) *iam.Role {
	for _,r := range roles {
		if *r.RoleName == name {
			return r
		}
	}
	return nil
}

func (applier *Applier) lookupAttachedPolicy(roles []*iam.AttachedPolicy, name string) *iam.AttachedPolicy {
	for _,r := range roles {
		if *r.PolicyName == name {
			return r
		}
	}
	return nil
}

func (applier *Applier) detachAndDeletePolicy(role *iam.Role, attachedPolicy *iam.AttachedPolicy) error {

	err := applier.client.detachPolicy(role, attachedPolicy)

	if err != nil {
		return fmt.Errorf("Failed detaching policy %s: %w", *attachedPolicy.PolicyName, err)
	}

	err = applier.client.DeleteAttachedPolicy(attachedPolicy)

	if err != nil {
		return fmt.Errorf("Failed deleting policy %s: %w", *attachedPolicy.PolicyName, err)
	}

	return nil
}

func (applier *Applier) createAndAttachPolicy(role *iam.Role, name string, document string) error {

	policy, err := applier.client.createOrUpdatePolicy(name, document)

	if err != nil {
		return fmt.Errorf("Failed creating policy %s: %w", name, err)
	}

	err = applier.client.attachPolicy(role, policy)

	if err != nil {
		return fmt.Errorf("Failed attaching policy %s: %w", name, err)
	}

	return nil
}

func (applier *Applier) createRole(name string) (*iam.Role, error) {
	return applier.client.CreateOrUpdateLoginRole(name)
}

func (applier *Applier) updateRole(desiredRole *iamCore.AwsRole, currentRole *iam.Role, policyDocuments map[string]string) error {

	attachedPolicies, err := applier.client.ListAttachedPolicies(currentRole)

	if err != nil {
		return fmt.Errorf("Unable to list attached policies: %w", err)
	}

	for _, desiredPolicy := range desiredRole.DatabaseLoginPolicies {

		desiredPolicyDocument := applier.buildDatabaseLoginPolicyDocument(desiredPolicy)
		policyName :=  desiredPolicy.DatabaseUsername
		attachedPolicy := applier.client.lookupAttachedPolicy(attachedPolicies,policyName)

		if attachedPolicy != nil {
			if desiredPolicyDocument == policyDocuments[policyName] {
				log.Infof("No changes detected in policy %s", policyName)
			} else {
				applier.eventListener.handle(PolicyUpdated, policyName)
				log.Infof("Updating policy %s attached to %s", policyName, *currentRole.RoleName)

				err := applier.detachAndDeletePolicy(currentRole, attachedPolicy)
				if err != nil {
					return fmt.Errorf("Unable to detach and delete policy %s: %w", *attachedPolicy.PolicyName, err)
				}

				err = applier.createAndAttachPolicy(currentRole, policyName, desiredPolicyDocument)
				if err != nil {
					return fmt.Errorf("Unable to create and attach policy %s: %w", policyName, err)
				}
			}
		} else {
			applier.eventListener.handle(PolicyCreated, policyName)
			log.Infof("Creating policy %s and attaching to %s", policyName, *currentRole.RoleName)
			err := applier.createAndAttachPolicy(currentRole, policyName, desiredPolicyDocument)

			if err != nil {
				return fmt.Errorf("Unable to create and attach policy %s: %w", policyName, err)
			}
		}
	}

	for _, attachedPolicy := range attachedPolicies {
		if desiredRole.LookupDatabaseLoginPolicyForUsername(*attachedPolicy.PolicyName) == nil {
			applier.eventListener.handle(PolicyDeleted, *attachedPolicy.PolicyName)
			log.Infof("Deleting policy %s attached to %s", *attachedPolicy.PolicyName, *currentRole.RoleName)

			err = applier.detachAndDeletePolicy(currentRole, attachedPolicy)

			if err != nil {
				return fmt.Errorf("Unable to detach and delete policy %s: %w", *attachedPolicy.PolicyName, err)
			}
		}
	}

	return nil
}

func (applier *Applier) deleteRole(role *iam.Role) error {

	attachedPolicies, err := applier.client.ListAttachedPolicies(role)

	if err != nil {
		return err
	}

	for _, attachedPolicy := range attachedPolicies {
		applier.eventListener.handle(PolicyDeleted, *attachedPolicy.PolicyName)
		log.Infof("Deleting policy %s attached to %s", *attachedPolicy.PolicyName, *role.RoleName)

		err = applier.detachAndDeletePolicy(role, attachedPolicy)

		if err != nil {
			return err
		}
	}

	return applier.client.DeleteLoginRole(role)
}

func (applier *Applier) Apply(model iamCore.Model) error {

	policyDocuments, err := applier.client.GetPolicyDocuments()

	if err != nil {
		return fmt.Errorf("Unable to list policy documents: %w", err)
	}

	existingRoles, err := applier.client.ListRoles()

	if err != nil {
		return fmt.Errorf("Unable to list roles: %w", err)
	}

	for _, desiredRole := range model.Roles {
		var err error

		existingRole := applier.lookupRole(existingRoles, desiredRole.Name)

		if existingRole == nil {
			applier.eventListener.handle(RoleCreated, desiredRole.Name)
			log.Infof("Creating role %s", desiredRole.Name)
			existingRole, err = applier.createRole(desiredRole.Name)

			if err != nil {
				return fmt.Errorf("Failed when creating role %s: %w", desiredRole.Name, err)
			}
		}

		applier.eventListener.handle(RoleUpdated, desiredRole.Name)
		log.Infof("Updating role %s", desiredRole.Name)
		err = applier.updateRole(desiredRole, existingRole, policyDocuments)
		if err != nil {
			return fmt.Errorf("Failed when updating role %s: %w", desiredRole.Name, err)
		}
	}

	for _, existingRole := range existingRoles {
		if model.LookupRole(*existingRole.RoleName) == nil {
			applier.eventListener.handle(RoleDeleted, *existingRole.RoleName)
			log.Infof("Deleting role %s", *existingRole.RoleName)
			err = applier.deleteRole(existingRole)

			if err != nil {
				return fmt.Errorf("Failed when deleting role %s: %w", *existingRole.RoleName, err)
			}
		}
	}

	return nil
}

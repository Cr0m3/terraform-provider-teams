# terraform-provider-teams

A Terraform / OpenTofu provider for **Microsoft Teams admin configuration** —
the `-Cs*` policy and settings surface behind the MicrosoftTeams PowerShell
module. Most resources are **generated** from the [`go-teams`](https://github.com/terraprovider/go-teams)
cmdlet catalog via the shared
[`tf-msadmin/genframework`](https://github.com/terraprovider/tf-msadmin) engine, so the
surface tracks the module. Group policy assignment is a hand-written generic
resource because one Terraform resource manages assignments across policy types.

> Not affiliated with or endorsed by Microsoft. It calls the same documented Teams
> admin API the PowerShell module does.

## Status

- **82 generated resources** (57 policy CRUD + 25 config/settings), each with a
  matching read-only data source, plus a generic group policy assignment resource.
- Authentication is aligned with the `hashicorp/azuread` and `azurerm` providers
  (same attributes, `ARM_*` / `AZURE_*` env fallbacks, every OIDC / workload-identity
  flavour) via `tf-msadmin/authschema`.

```hcl
terraform {
  required_providers {
    teams = { source = "terraprovider/teams" }
  }
}

provider "teams" {} # ARM_TENANT_ID / ARM_CLIENT_ID / ARM_CLIENT_SECRET (or cert / OIDC)

resource "teams_meeting_policy" "kiosk" {
  identity                = "KioskPolicy"
  allow_meet_now          = false
  allow_channel_meeting_scheduling = false
  meeting_chat_enabled_type        = "Disabled"
}

resource "teams_tenant_federation_configuration" "global" {
  allow_federated_users = true
  allow_teams_consumer  = false
}
```

App-only needs the app's service principal to hold a **Teams admin directory role**
(a `403` is a role gap, not a token problem).

## Group policy assignments

[`teams_group_policy_assignment`](docs/resources/group_policy_assignment.md)
uses a **POST** group grant for create, update, and null-policy removal, matching
the MicrosoftTeams module. PATCH is a separate update-only operation and returns
404 when the group has no assignment of that type. The resource explicitly selects
POST because the pinned `go-teams` v0.4.0 `PolicyGrant` transport selects PATCH.

The Terraform `rank` maps directly to the API's **`priority`** field (one-based,
with no offset). Grant requests send `{"policyName":"Name","priority":1}`; removal
sends `{"policyName":null}`. Reads take the actual rank from `priority`, not from
the configured value, so a rank normalized by Teams remains visible as drift.

Run the tenant-free lifecycle regression with OpenTofu:

```sh
TF_ACC_TERRAFORM_PATH=tofu TF_ACC_PROVIDER_HOST=registry.opentofu.org \
  go test -race -count=1 -run '^TestGroupPolicyAssignment_FakeServiceLifecycle$' -v ./internal/provider/
```

The fake service rejects PATCH for missing assignments and uses the live API's
camel-case fields, including `priority`. This checks the provider lifecycle, not
live Teams permissions or propagation behavior.

## Generation

```
go-teams/spec catalog  --cmd/gen-tf-->  genframework.Resource  --Generate-->  internal/provider/*.go
```

`cmd/gen-tf` is the Teams frontend: it maps the live-validated `/Skype.Policy`
nouns to normalized resources (bool attributes set `PointerParam` for the tri-state
`Nullable<T>` settings). Generated files are `*_resource.go` / `*_data_source.go` /
`zz_generated_resources.go` — do not edit; re-run `go run ./cmd/gen-tf`.

## License

MIT — see [LICENSE](LICENSE). Not affiliated with or endorsed by Microsoft.

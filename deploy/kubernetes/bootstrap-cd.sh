#!/usr/bin/env bash
# One-time operator setup. Never called by the deployment workflow itself.
set -euo pipefail
project=machinist-prod
pool=machinist-deployment
provider=machinist-main
service_account=machinist-deployer@machinist-prod.iam.gserviceaccount.com
context=gke_machinist-prod_europe-west2_machinist-runners
gcloud services enable iamcredentials.googleapis.com sts.googleapis.com --project "$project"
number=$(gcloud projects describe "$project" --format='value(projectNumber)')
if ! gcloud iam service-accounts describe "$service_account" --project "$project" >/dev/null 2>&1; then
  gcloud iam service-accounts create machinist-deployer --project "$project" --display-name='Machinist GitHub deployment'
fi
if ! gcloud iam workload-identity-pools describe "$pool" --project "$project" --location global >/dev/null 2>&1; then
  gcloud iam workload-identity-pools create "$pool" --project "$project" --location global --display-name='Machinist deployment only'
fi
verb=create-oidc
if gcloud iam workload-identity-pools providers describe "$provider" --project "$project" --location global --workload-identity-pool "$pool" >/dev/null 2>&1; then
  verb=update-oidc
fi
other=$(gcloud iam workload-identity-pools providers list --project "$project" --location global --workload-identity-pool "$pool" --format='value(name)' | sed "\|/providers/$provider$|d")
test -z "$other" || { echo 'Dedicated deployment pool contains another provider; refusing shared trust.' >&2; exit 1; }
gcloud iam workload-identity-pools providers "$verb" "$provider" --project "$project" --location global --workload-identity-pool "$pool" \
  --issuer-uri=https://token.actions.githubusercontent.com \
  --attribute-mapping='google.subject=assertion.sub,attribute.repository_id=assertion.repository_id' \
  --attribute-condition="assertion.repository_id=='1302544971' && assertion.repository_owner_id=='733944' && assertion.ref=='refs/heads/main' && assertion.workflow_ref=='owainlewis/machinist/.github/workflows/deploy-gke.yml@refs/heads/main'"
gcloud iam service-accounts add-iam-policy-binding "$service_account" --project "$project" \
  --role=roles/iam.workloadIdentityUser \
  --member="principalSet://iam.googleapis.com/projects/$number/locations/global/workloadIdentityPools/$pool/attribute.repository_id/1302544971" >/dev/null
gcloud projects add-iam-policy-binding "$project" --role=roles/container.clusterViewer --member="serviceAccount:$service_account" --condition=None >/dev/null
gcloud artifacts repositories add-iam-policy-binding machinist --project "$project" --location europe-west2 --role=roles/artifactregistry.writer --member="serviceAccount:$service_account" >/dev/null
kubectl --context "$context" apply -f deploy/kubernetes/cd-resources.yaml
gh api --method PUT repos/owainlewis/machinist/environments/production >/dev/null
gh variable set GCP_WORKLOAD_IDENTITY_PROVIDER --repo owainlewis/machinist --env production --body "projects/$number/locations/global/workloadIdentityPools/$pool/providers/$provider"
gh variable set GCP_DEPLOY_SERVICE_ACCOUNT --repo owainlewis/machinist --env production --body "$service_account"
printf 'Configured main-only keyless deployment for owainlewis/machinist.\n'

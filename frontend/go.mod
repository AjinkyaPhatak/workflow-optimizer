// This file is not Go code for the frontend. It marks frontend/ as a separate
// module so the backend's `go build/test/vet ./...` never walks into
// frontend/node_modules (some npm packages ship .go files).
module workflow-optimizer/frontend

go 1.22

#!/bin/bash
# Fix AlertFilters calls in tests to include Scope

file="internal/panel/observability/alerts_test.go"

# Replace patterns without Scope to include Scope
sed -i 's/AlertFilters{State: /AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, State: /g' "$file"
sed -i 's/AlertFilters{Limit: /AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, Limit: /g' "$file"
sed -i 's/AlertFilters{AlertType: /AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, AlertType: /g' "$file"
sed -i 's/AlertFilters{Severity: /AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, Severity: /g' "$file"
sed -i 's/AlertFilters{TargetType: /AlertFilters{Scope: rbac.Scope{AdminID: 1, IsSuper: true}, TargetType: /g' "$file"

echo "Updated $file"

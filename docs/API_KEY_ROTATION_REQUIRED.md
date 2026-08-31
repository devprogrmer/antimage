# API Key Rotation Required

**Date Identified:** 2026-08-26  
**Severity:** HIGH  
**Reference:** HANDOFF.MD §6.1

## Issue

A file `Claude.reg` previously existed in the repository root containing a **live inference-gateway API key**. While the file:
- Was never committed to git history
- Is now absent from the working tree
- Has been added to `.gitignore` as `*.reg`

The key sat in a working directory and must be treated as exposed.

## Action Required

**The API key must be rotated immediately.**

1. Access the inference-gateway admin console
2. Revoke the exposed API key
3. Generate a new key
4. Update any systems using the old key
5. Securely store the new key (never in the repository)

## Prevention

- `*.reg` files are now gitignored
- Never store credentials in repository working directories
- Use environment variables or secure secret management
- Follow the established pattern: credentials sealed with `internal/shared/secrets.Box`

## Status

⚠️ **PENDING USER ACTION** - Only the repository owner can rotate this key.

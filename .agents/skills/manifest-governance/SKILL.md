---
name: manifest-governance
description: SOP for managing and synchronizing agent skills via manifest, lock, and mirror configurations.
---

## Trigger

This skill is triggered when the agent needs to add, remove, update, or synchronize agent skills, or when detecting and fixing configuration drifts.

## Entry Point

The entry point is updating `.manifest.json` (or the default manifest file specified by `SKILLS_MANIFEST`).

## Workflow

1. **Modify Manifest**: When a new skill is requested, edit `.manifest.json` (or equivalent) to declare the skill name, target, and source (repository, ref, path).
2. **Synchronize**: Run `skills sync [name]` to reconcile the manifest and update `.lock.json` with the pinned commit.
3. **Verify Mirror/Drift**: Run `skills doctor` to ensure manifest, lock, disk, and mirror directories are in sync and free of drifts.
4. **Update**: To update a skill's pin to the latest remote commit, run `skills update [name]`.

## Verification

- Run `skills doctor` to verify the state and check for any configuration drifts.
- Run `skills list` to list all skills and verify their installation status.
- Run `skills info <name>` to verify source, path, commit, and disk location details for a specific skill.

## Guardrails

- Never manually edit `.lock.json`. Always let `skills sync` or `skills update` generate and update the lock file.
- Verify symlinks and mirror paths using `skills doctor` after any modifications to the `directories` or `mirrors` sections of `.manifest.json`.
- Ensure the skill's name matches its source directory name.

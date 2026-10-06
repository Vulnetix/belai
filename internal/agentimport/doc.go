// Package agentimport brings an agent definition written for another agent
// harness into Belai as an ordinary agent profile.
//
// Four formats are read: a Claws package (a CLAW.md and its sidecar files), an
// NVIDIA NeMo Fabric agent configuration ("nemoclaw"), a Hermes profile
// (a directory or a .tar.gz export) and a mini-SWE-agent YAML configuration.
// None of them is a standard, so each has an adapter that maps what Belai's
// schema has a field for and keeps the rest as metadata or names it in the
// report.
//
// An import is untrusted input from a file the user chose. It is read through
// bounded readers (no symlink, no file over 1 MiB, no archive path outside the
// archive, no credential file by name), every string is sanitised, and the
// result goes through the profile validator like any other profile. The rules a
// foreign definition can never change:
//
//   - the profile is a single-mode, supervised agent: no worker blocks, no
//     schedule, no guardrails or ask_permission override, no facts or knowledge;
//   - tools come only from a fixed name table, and a definition that names no
//     tool Belai knows gets the read-only set rather than every tool;
//   - an endpoint, a credential or an environment variable name is never
//     carried over, and nothing from an MCP server entry becomes a command;
//   - what has no field is kept as profile metadata (data for people, never
//     shown to a model) or only named in the report.
//
// The package writes nothing. The caller shows the [Result] notes to the user
// and decides whether to save the profile and install the skills.
package agentimport

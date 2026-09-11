# Independent loader in target workspaces

While configuring project-isolated Forge instances, 26 Fornax-prefixed workspaces
were checked. `fornax-pi` failed because its tsconfig path aliases redirected
Forge's runtime dependency imports into that workspace's incomplete source tree.

A regression fixture first reproduced failure using a foreign tsconfig mapping
`undici`, `typebox` and the Pi AI package to a poison module. Both executable
entrypoints now register tsx with `tsconfig: false`, keeping Forge imports
independent of the working repository. This does not change the child Codex
working directory, or export a tsconfig override into its tools.

Validation: Node test suite and typecheck; all 26 workspace project queries;
separate Forge/Fornax project UIDs and credentials, and mutual cross-credential
rejection over real HTTP. Directory, explicit endpoint/token and inherited
runtime mismatch checks fail closed in the local installation wrapper. The local
installation uses separate single-project daemons; it does not add a multi-project
router to forged or alter Kata storage semantics.

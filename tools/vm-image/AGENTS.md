# VM Image Agent Instructions

This directory owns the yeet Ubuntu VM image builders and documentation.

## Ubuntu Compatibility

- Preserve Ubuntu package and filesystem contracts. Do not relocate
  package-owned files or replace package-owned directories unless Ubuntu's
  packaging system performs that change.
- Do not do cosmetic status cleanup by moving binaries between `/usr/bin`,
  `/usr/sbin`, `/bin`, or `/sbin`.
- Treat `systemctl status` taints as diagnostic signals. Classify the source
  first: yeet-caused failed units should be fixed, while upstream Ubuntu layout
  warnings may be documented or accepted.
- Optimize boot with compatible mechanisms: package removal, service masks,
  kernel config, yeet-owned init/readiness code, metadata, sysctls, and
  tmpfiles.
- Keep `tools/vm-image/README.md`, build validation, and release notes aligned
  with intentional image policy changes.

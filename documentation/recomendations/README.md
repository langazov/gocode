# Design recommendations

[← Documentation index](../README.md)

---

The numbered chapters in `documentation/` explain **how the code is built**.
These documents explain **how the product should look and behave** — the visual
and interaction contract a change is measured against.

They are **normative**. Where one describes current behaviour, it is because
that behaviour is the reference; where it says *should*, that is a rule for new
work. A reviewer is entitled to cite one of these against a diff.

## Index

| Surface | Document | Covers |
|---|---|---|
| TUI | [TUI recommendations](TUI_RECOMENDATIONS.md) | Rendering model, theme tokens, geometry, glyph vocabulary, every view and dialog layout, controls, keyboard and mouse models, responsiveness, performance, anti-patterns, checklists |
| Permissions | [Permissions recommendations](PERMISSIONS_RECOMENDATIONS.md) | Rule semantics and precedence, policy sources, the ask lifecycle and save granularity, `external_directory`, tool advertisement, subagent inheritance, prompt surface contract (banner content, always-confirmation, reject-with-reason), HTTP API, auto-accept, bypass tiers, persistence and revocation, anti-patterns, checklists |

## The rule

**Every design recommendation document lives in this directory.** Not beside
the code it governs, not at the repository root, not in `docs/` (that is the
published website). One place, so a design question has one place to be
answered and reviewers know where to look.

## Conventions for a document here

- **One file per surface**, named `<SURFACE>_RECOMENDATIONS.md`. Add it to the
  index table above in the same commit.
- **Normative, not descriptive.** State the rule, then the reason. A rule with
  no reason gets "fixed" by the next person who disagrees with it.
- **Grounded in the code.** Every constant, breakpoint and colour token is
  quoted from the implementation, not invented. Measure before asserting a
  number.
- **Anti-patterns carry their history.** A "do not do this" is worth far more
  when it says what broke, which is why those sections read like a bug list.
- **Kept in sync.** A change that alters a documented rule updates the document
  in the same commit. A document that has drifted is worse than none, because
  it is still cited.
- **Cross-linked from the chapter it governs**, so someone reading the
  architecture doc finds the design contract without knowing to look for it.

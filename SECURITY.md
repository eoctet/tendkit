# Security Policy

[English](SECURITY.md) | [简体中文](SECURITY_ZH_CN.md)

## Supported versions

TendKit is pre-release software. Security fixes are made for the latest tagged release and the current `main` branch only. Older releases and unmaintained branches do not receive security updates.

| Version | Supported |
| --- | --- |
| Latest tagged release | Yes |
| `main` | Yes |
| Older releases or branches | No |

This policy may change when the project adopts a stable release cadence.

## Reporting a vulnerability

Do not open a public issue, discussion, or pull request for an undisclosed vulnerability.

Use GitHub's [private vulnerability reporting](https://github.com/eoctet/tendkit/security/advisories/new) to send the report to the maintainers. If that link is unavailable, open a public issue containing no vulnerability details or sensitive information and ask the maintainers to establish a private contact channel.

Include as much of the following as is safe and available:

- affected version, commit, operating system, and architecture;
- affected component and security boundary;
- reproduction steps or a minimal proof of concept;
- expected and observed behavior;
- potential impact and required preconditions;
- suggested mitigation or fix, if known;
- whether the issue has been disclosed elsewhere;
- a safe way to contact you for follow-up.

Do not include real credentials, tokens, cryptographic keys, personal data, or production configuration. Use synthetic data and redact logs, paths, environment variables, and command output.

## What to expect

The maintainers aim to:

- acknowledge a complete report within 3 business days;
- provide an initial assessment or request additional information within 10 business days;
- send a status update at least every 7 days while an accepted report remains unresolved.

These are response targets, not a guaranteed remediation deadline. Resolution time depends on severity, reproducibility, supported platforms, and the coordination needed for a safe release.

After assessment, the maintainers will tell the reporter whether the issue is accepted, requires more information, is already known, or is outside this policy. For an accepted vulnerability, the maintainers will coordinate validation, mitigation, release timing, and disclosure. Please allow a reasonable remediation period before public disclosure.

## Scope

Security reports may include, but are not limited to:

- command or argument injection through configuration, templates, provider data, or discovered metadata;
- unsafe propagation or logging of credentials, tokens, cryptographic keys, or environment variables;
- insecure configuration ownership, permissions, locking, or atomic persistence behavior;
- download source confusion, path traversal, unsafe asset selection, or integrity-verification bypass;
- cancellation or process-group failures that leave privileged or destructive child processes running;
- a network or parser issue that crosses an explicitly documented trust boundary.

The following are generally not vulnerabilities by themselves:

- a trusted, user-authored Provider action executing the command it explicitly contains;
- availability of a newer tool version without an update capability;
- unsupported operating systems, architectures, or old TendKit releases;
- social engineering, physical access, or compromise of the user's account or workstation;
- dependency findings without a demonstrated path that affects a supported TendKit version.

If you are unsure whether an issue is security-sensitive, report it privately.

## Safe research

Test only systems and data you own or are authorized to use. Avoid privacy violations, service disruption, data destruction, persistence, and access beyond what is necessary to demonstrate the issue. Do not test against other users or third-party services. A report made in good faith and consistent with these rules will be handled as security research rather than abuse.

## Disclosure and credit

The project prefers coordinated disclosure. Once a fix or mitigation is available, the maintainers may publish a GitHub Security Advisory and release notes describing affected versions, impact, and remediation without exposing unnecessary exploit detail. Reporter credit will be included when requested and appropriate; anonymous reporting and anonymous credit are also respected.

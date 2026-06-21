# Security Policy

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

This project handles health data (FHIR resources), so we take security reports
seriously. If you discover a vulnerability:

- Report it privately to the WSO2 security team at **security@wso2.com**, or
- Use GitHub's [private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability)
  ("Report a vulnerability" under the **Security** tab of this repository).

Please include:

- A description of the vulnerability and its potential impact.
- Steps to reproduce (requests, configuration, affected endpoints).
- The affected version or commit.

We will acknowledge your report, keep you updated on remediation progress, and
coordinate disclosure with you.

See the [WSO2 Security guidelines](https://security.docs.wso2.com/en/latest/security-reporting/vulnerability-reporting-guidelines/)
for more details on WSO2's responsible-disclosure process.

## Supported versions

This project is under active development. Security fixes are applied to `main`.
Until a formal release process is established, please track `main` for the
latest fixes.

## Handling secrets

- Never commit credentials, tokens, or keys. Provide secrets such as
  `DB_PASSWORD` via environment variables (see the README configuration
  reference), not the checked-in config file.

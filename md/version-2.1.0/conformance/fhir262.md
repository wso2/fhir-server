---
title: FHIR262 conformance report
description: Browse the latest FHIR conformance test suite (https://github.com/HealthSamurai/fhir262) run for WSO2 FHIR Server.
---

import Fhir262Report from '@site/src/components/Fhir262Report';

# FHIR262 conformance report

Copyright 2026 The fhir262 Contributors

This report runs every publicly accessible server adapter from the
[FHIR262 suite](https://github.com/HealthSamurai/fhir262)
including WSO2 FHIR Server against the public `ghcr.io/wso2/fhir-server:latest`
image. This runs a temporary fork of the suite until
[our upstream PR](https://github.com/HealthSamurai/fhir262/pull/2) is merged. Run it manually
from the repository's **Actions** tab when a refreshed result is needed.

<Fhir262Report />

If the report has not been published yet, the embedded page will be unavailable until
the first successful manual workflow run.

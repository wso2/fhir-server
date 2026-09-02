#!/usr/bin/env python3
# Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
#
# WSO2 LLC. licenses this file to you under the Apache License,
# Version 2.0 (the "License"); you may not use this file except
# in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing,
# software distributed under the License is distributed on an
# "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
# KIND, either express or implied. See the License for the
# specific language governing permissions and limitations
# under the License.

"""Regenerate the embedded FHIR R4 definition bundles in internal/basedef.

The two .gz artifacts under internal/basedef are verbatim transformations of
the frozen HL7 FHIR R4 (v4.0.1) publication and must never be hand-edited —
run this script to (re)produce them, e.g. after bumping to a new FHIR release:

    make refresh-definitions            # or: python3 scripts/refresh-definitions.py

Per-file policy (mirrors what each Go loader consumes):

  profiles-resources.min.json.gz  <- {BASE}/profiles-resources.json
      The full official bundle, minified. Filtering to base resource
      definitions (kind=resource, derivation=specialization) happens at load
      time in basedef.decode(), so the artifact stays a faithful copy.

  profiles-types.min.json.gz      <- {BASE}/profiles-types.json
      Reduced to the base complex-datatype StructureDefinitions only
      (kind=complex-type, derivation=specialization — the same filter
      basedef.decodeDatatypes() re-applies), with narrative `text` stripped.
      This is what lets the artifact stay small enough (~85 KB) to embed.

Output is deterministic: minified JSON (ASCII-escaped, no trailing newline)
gzipped with mtime=0, so reruns against the same publication are no-ops and
the artifacts can be verified by regenerating and diffing.

Uses only the Python standard library.
"""

import gzip
import hashlib
import io
import json
import sys
import urllib.request
from pathlib import Path

BASE = "https://hl7.org/fhir/R4"
OUT_DIR = Path(__file__).resolve().parent.parent / "internal" / "basedef"


def fetch(url: str) -> dict:
    print(f"downloading {url} ...", file=sys.stderr)
    with urllib.request.urlopen(url) as resp:
        return json.load(resp)


def reduce_types(bundle: dict) -> dict:
    """Keep only base complex-datatype StructureDefinitions, drop narratives."""
    entries = []
    for e in bundle.get("entry", []):
        sd = e.get("resource") or {}
        if sd.get("resourceType") != "StructureDefinition":
            continue
        if sd.get("kind") != "complex-type":
            continue
        if sd.get("derivation") != "specialization":
            continue
        sd.pop("text", None)
        entries.append({"resource": sd})
    return {"resourceType": "Bundle", "type": "collection", "entry": entries}


def write_gz(path: Path, payload: dict) -> None:
    raw = json.dumps(payload, separators=(",", ":")).encode()
    buf = io.BytesIO()
    # mtime=0 keeps the archive byte-stable across reruns.
    with gzip.GzipFile(fileobj=buf, mode="wb", compresslevel=9, mtime=0) as gz:
        gz.write(raw)
    path.write_bytes(buf.getvalue())
    digest = hashlib.sha256(raw).hexdigest()
    print(f"wrote {path.name}: {len(buf.getvalue()):,} bytes gz "
          f"({len(raw):,} raw, sha256 {digest[:16]}…)", file=sys.stderr)


def main() -> None:
    write_gz(OUT_DIR / "profiles-resources.min.json.gz",
             fetch(f"{BASE}/profiles-resources.json"))
    write_gz(OUT_DIR / "profiles-types.min.json.gz",
             reduce_types(fetch(f"{BASE}/profiles-types.json")))


if __name__ == "__main__":
    main()

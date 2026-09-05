{{/*
Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).

WSO2 LLC. licenses this file to you under the Apache License,
Version 2.0 (the "License"); you may not use this file except
in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing,
software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
KIND, either express or implied. See the License for the
specific language governing permissions and limitations
under the License.
*/}}

{{/*
Expand the name of the chart.
*/}}
{{- define "fhir-server.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "fhir-server.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "fhir-server.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "fhir-server.labels" -}}
helm.sh/chart: {{ include "fhir-server.chart" . }}
{{ include "fhir-server.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "fhir-server.selectorLabels" -}}
app.kubernetes.io/name: {{ include "fhir-server.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Name of the ServiceAccount to use.
*/}}
{{- define "fhir-server.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "fhir-server.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding the database DSN, whichever path is active.
*/}}
{{- define "fhir-server.databaseSecretName" -}}
{{- if .Values.database.existingSecret.name }}
{{- .Values.database.existingSecret.name }}
{{- else }}
{{- include "fhir-server.fullname" . }}
{{- end }}
{{- end }}

{{/*
Key within the database Secret holding the DSN.
*/}}
{{- define "fhir-server.databaseSecretKey" -}}
{{- if .Values.database.existingSecret.name }}
{{- .Values.database.existingSecret.key | default "database-url" }}
{{- else -}}
database-url
{{- end }}
{{- end }}

{{/*
Fixed mount path for the IG package cache. Always volume-backed (PVC or emptyDir)
so the container root filesystem can stay read-only regardless of whether
persistence.igCache is enabled.
*/}}
{{- define "fhir-server.igCachePath" -}}
/data/fhir-ig-cache
{{- end }}

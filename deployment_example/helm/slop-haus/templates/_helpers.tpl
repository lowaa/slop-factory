{{- define "slop-haus.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "slop-haus.labels" -}}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ include "slop-haus.tag" . | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/* Selector labels for one component: pass (list $ "api"). */}}
{{- define "slop-haus.selectorLabels" -}}
{{- $root := index . 0 -}}
app.kubernetes.io/name: {{ $root.Chart.Name }}
app.kubernetes.io/instance: {{ $root.Release.Name }}
app.kubernetes.io/component: {{ index . 1 }}
{{- end -}}

{{- define "slop-haus.tag" -}}
{{- .Values.image.tag | default .Chart.AppVersion -}}
{{- end -}}

{{/* The moving `main` tag is re-pulled; a pinned one is not. */}}
{{- define "slop-haus.pullPolicy" -}}
{{- if .Values.image.pullPolicy -}}
{{- .Values.image.pullPolicy -}}
{{- else if eq (include "slop-haus.tag" .) "main" -}}
Always
{{- else -}}
IfNotPresent
{{- end -}}
{{- end -}}

{{- define "slop-haus.secretName" -}}
{{- .Values.secrets.existingSecret | default (printf "%s-secrets" (include "slop-haus.fullname" .)) -}}
{{- end -}}

{{- define "slop-haus.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- .Values.serviceAccount.name | default (include "slop-haus.fullname" .) -}}
{{- else -}}
{{- .Values.serviceAccount.name | default "default" -}}
{{- end -}}
{{- end -}}

{{/* A resource name: pass (list $ "suffix"). Truncated after the suffix, as names are DNS labels. */}}
{{- define "slop-haus.componentName" -}}
{{- printf "%s-%s" (include "slop-haus.fullname" (index . 0)) (index . 1) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "slop-haus.apiName" -}}
{{- include "slop-haus.componentName" (list . "api") -}}
{{- end -}}

{{- define "slop-haus.postgresName" -}}
{{- include "slop-haus.componentName" (list . "postgresql") -}}
{{- end -}}

{{/* An image reference: pass (list $ "api"). A digest, when set, pins it past tag pruning. */}}
{{- define "slop-haus.image" -}}
{{- $root := index . 0 -}}
{{- $img := index $root.Values.image (index . 1) -}}
{{- if $img.digest -}}
{{- printf "%s@%s" $img.repository $img.digest -}}
{{- else -}}
{{- printf "%s:%s" $img.repository (include "slop-haus.tag" $root) -}}
{{- end -}}
{{- end -}}

{{/* An env entry read from the chart's (or the existing) Secret. */}}
{{- define "slop-haus.secretEnv" -}}
- name: {{ .name }}
  valueFrom:
    secretKeyRef:
      name: {{ include "slop-haus.secretName" .root }}
      key: {{ .name }}
{{- end -}}

{{/*
Refuses values the api would refuse at startup anyway, so the mistake shows
up at install time rather than as a crash loop.
*/}}
{{- define "slop-haus.validate" -}}
{{- $v := .Values -}}
{{- if not $v.publicBaseUrl -}}
{{- fail "publicBaseUrl is required: the https address the board is served at" -}}
{{- end -}}
{{- if not (regexMatch "^https?://[^/]+" $v.publicBaseUrl) -}}
{{- fail "publicBaseUrl must be an absolute http(s) URL" -}}
{{- end -}}
{{- if and (hasPrefix "http://" $v.publicBaseUrl) (not $v.api.allowInsecureHttp) (not (regexMatch "^http://(localhost|127\\.[0-9.]+|\\[::1\\])(:[0-9]+)?(/|$)" $v.publicBaseUrl)) -}}
{{- fail "publicBaseUrl is plain http on a non-loopback host: use https, or set api.allowInsecureHttp for a throwaway install" -}}
{{- end -}}
{{- if not (has $v.api.email.backend (list "log" "smtp")) -}}
{{- fail "api.email.backend must be log or smtp" -}}
{{- end -}}
{{- if and (eq $v.api.email.backend "smtp") (or (not $v.api.email.smtp.host) (not $v.api.email.from)) -}}
{{- fail "api.email.backend=smtp needs api.email.smtp.host and api.email.from" -}}
{{- end -}}
{{- if and (not $v.api.passwordSignIn) (not $v.api.github.clientId) (not $v.api.oidc.issuerUrl) -}}
{{- fail "api.passwordSignIn=false needs api.github or api.oidc configured, or nobody can sign in" -}}
{{- end -}}
{{- if and $v.api.webhook.enabled (not $v.api.webhook.publicBaseUrl) -}}
{{- fail "api.webhook.enabled needs api.webhook.publicBaseUrl" -}}
{{- end -}}
{{- if and $v.api.webhook.enabled (not $v.api.credentials.enabled) -}}
{{- fail "api.webhook.enabled needs api.credentials.enabled: the api seals each App's webhook secret under that key, and starts no webhook listener without it" -}}
{{- end -}}
{{- if and $v.api.intake.enabled (not $v.api.intake.publicBaseUrl) -}}
{{- fail "api.intake.enabled needs api.intake.publicBaseUrl" -}}
{{- end -}}
{{- range $name, $url := dict "api.webhook.publicBaseUrl" (ternary $v.api.webhook.publicBaseUrl "" $v.api.webhook.enabled) "api.intake.publicBaseUrl" (ternary $v.api.intake.publicBaseUrl "" $v.api.intake.enabled) -}}
{{- if and $url (not (regexMatch "^https?://[^/?#@]+/?$" $url)) -}}
{{- fail (printf "%s must be an http(s) origin with no path, got %q" $name $url) -}}
{{- end -}}
{{- end -}}
{{- if and $v.api.webhook.enabled (hasPrefix "http://" $v.api.webhook.publicBaseUrl) (not $v.api.allowInsecureHttp) -}}
{{- fail "api.webhook.publicBaseUrl must be https: GitHub would send every delivery in the clear" -}}
{{- end -}}
{{- if and (or $v.api.oidc.issuerUrl $v.api.oidc.clientId) (not (and $v.api.oidc.issuerUrl $v.api.oidc.clientId)) -}}
{{- fail "api.oidc.issuerUrl and api.oidc.clientId must be set together" -}}
{{- end -}}
{{- if and $v.api.attachments.enabled (not $v.api.attachments.endpoint) -}}
{{- fail "api.attachments.enabled needs api.attachments.endpoint" -}}
{{- end -}}
{{- if not $v.secrets.existingSecret -}}
{{- if not $v.secrets.appDbPassword -}}
{{- fail "secrets.appDbPassword is required (or secrets.existingSecret)" -}}
{{- end -}}
{{- if lt (len $v.secrets.jwtSecret) 32 -}}
{{- fail "secrets.jwtSecret is required and must be at least 32 bytes (or secrets.existingSecret)" -}}
{{- end -}}
{{- if and (not $v.secrets.databaseUrl) (not $v.postgresql.enabled) -}}
{{- fail "secrets.databaseUrl is required unless postgresql.enabled (or secrets.existingSecret)" -}}
{{- end -}}
{{- if and $v.postgresql.enabled (not $v.postgresql.password) -}}
{{- fail "postgresql.password is required with postgresql.enabled (or secrets.existingSecret)" -}}
{{- end -}}
{{- if and $v.api.github.clientId (not $v.secrets.githubClientSecret) -}}
{{- fail "api.github.clientId needs secrets.githubClientSecret" -}}
{{- end -}}
{{- if and $v.api.oidc.issuerUrl (not $v.secrets.oidcClientSecret) -}}
{{- fail "api.oidc.issuerUrl needs secrets.oidcClientSecret" -}}
{{- end -}}
{{- if and $v.api.credentials.enabled (not $v.secrets.credentialEncryptionKey) -}}
{{- fail "api.credentials.enabled needs secrets.credentialEncryptionKey" -}}
{{- end -}}
{{- if and $v.api.intake.enabled $v.secrets.intakeKey (or (lt (len $v.secrets.intakeKey) 32) (eq $v.secrets.intakeKey $v.secrets.jwtSecret)) -}}
{{- fail "secrets.intakeKey must be at least 32 bytes and differ from secrets.jwtSecret" -}}
{{- end -}}
{{- if and $v.api.attachments.enabled (not $v.secrets.s3Credentials) -}}
{{- fail "api.attachments.enabled needs secrets.s3Credentials" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
An Ingress to one Service: pass root, ingress (values), name, service, port,
and baseUrl, whose host is the rule's when the values name none.
*/}}
{{- define "slop-haus.ingress" -}}
{{- $in := .ingress -}}
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: {{ .name }}
  labels:
    {{- include "slop-haus.labels" .root | nindent 4 }}
  {{- with $in.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
spec:
  {{- with $in.className }}
  ingressClassName: {{ . }}
  {{- end }}
  {{- with $in.tls }}
  tls:
    {{- toYaml . | nindent 4 }}
  {{- end }}
  rules:
    - host: {{ $in.host | default (urlParse .baseUrl).hostname | quote }}
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: {{ .service }}
                port:
                  number: {{ .port }}
{{- end -}}

Third-party software notices for git-dirty

git-dirty is distributed under the MIT License (see LICENSE). It includes the
following Go modules, each under its own license reproduced below.
{{ range . }}
================================================================================
{{ .Name }}
License: {{ .LicenseName }}
Source: {{ .LicenseURL }}
--------------------------------------------------------------------------------

{{ .LicenseText }}
{{ end }}

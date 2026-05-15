%global goipath github.com/decko/soda

Name:           soda-minimal
Version:        0.6.0
Release:        1%{?dist}
Summary:        AI-powered development pipeline orchestrator (no sandbox)

License:        Apache-2.0
URL:            https://%{goipath}
Source0:        https://%{goipath}/archive/v%{version}/soda-%{version}.tar.gz
Source1:        soda-%{version}-vendor.tar.bz2

ExclusiveArch:  x86_64 aarch64

BuildRequires:  golang >= 1.24
BuildRequires:  git-core

Conflicts:      soda

# Static Go binary stripped with -s -w; no debug symbols to extract.
%global debug_package %{nil}

%description
SODA is a configurable AI coding pipeline that turns tickets into PRs.
Each phase runs in a fresh, sandboxed Claude Code session with structured
output. This is the minimal build without CGO sandbox support.

%prep
%autosetup -n soda-%{version}
tar -xf %{SOURCE1}

%build
export GOFLAGS="-mod=vendor"
export CGO_ENABLED=0
go build -trimpath \
    -ldflags "-s -w -X main.version=%{version}" \
    -o soda ./cmd/soda

# Generate shell completions
./soda completion bash > soda.bash
./soda completion zsh  > _soda
./soda completion fish > soda.fish

%install
install -Dpm 0755 soda %{buildroot}%{_bindir}/soda
install -Dpm 0644 soda.bash %{buildroot}%{_datadir}/bash-completion/completions/soda
install -Dpm 0644 _soda     %{buildroot}%{_datadir}/zsh/site-functions/_soda
install -Dpm 0644 soda.fish %{buildroot}%{_datadir}/fish/vendor_completions.d/soda.fish

%check
export GOFLAGS="-mod=vendor"
export CGO_ENABLED=0
export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_SYSTEM=/dev/null
# Skip Jira smoke tests (require wtmcp binary) and sandbox tests (require CGO).
go test $(go list ./... | grep -v -E 'ticket|sandbox') 

%files
%license LICENSE
%doc README.md CHANGELOG.md config.example.yaml
%{_bindir}/soda
%{_datadir}/bash-completion/completions/soda
%{_datadir}/zsh/site-functions/_soda
%{_datadir}/fish/vendor_completions.d/soda.fish

%changelog
* Fri May 15 2026 decko de Brito <ddebrito@redhat.com> - 0.6.0-1
- Initial COPR package (minimal build, no sandbox)

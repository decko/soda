%global soda_version %{?soda_version}%{!?soda_version:0.0.0}
%global goipath         github.com/decko/soda
%global gomodcache      %{_builddir}/gomodcache

Name:           soda-minimal
Version:        %{soda_version}
Release:        1%{?dist}
Summary:        AI-powered software development pipeline orchestrator (no sandbox)

License:        MIT
URL:            https://%{goipath}
Source0:        soda-%{soda_version}.tar.gz

ExclusiveArch:  x86_64 aarch64

BuildRequires:  golang >= 1.25

Conflicts:      soda

%description
Soda is an AI-powered software development pipeline orchestrator that drives
Claude Code through multi-phase workflows. This is a minimal, statically
linked build without CGO. Sandbox enforcement (Landlock, seccomp, cgroups)
is not available in this variant.

%prep
%setup -q -n soda-%{soda_version}

%build
export GOMODCACHE=%{gomodcache}
export GOFLAGS="-mod=mod"
export CGO_ENABLED=0

go mod download

go build -ldflags "-s -w -X main.version=%{soda_version}" \
    -o soda ./cmd/soda

# Generate shell completions
./soda completion bash > soda.bash
./soda completion zsh  > _soda
./soda completion fish > soda.fish

%install
install -Dpm 0755 soda %{buildroot}%{_bindir}/soda

# Shell completions
install -Dpm 0644 soda.bash %{buildroot}%{_datadir}/bash-completion/completions/soda
install -Dpm 0644 _soda     %{buildroot}%{_datadir}/zsh/site-functions/_soda
install -Dpm 0644 soda.fish %{buildroot}%{_datadir}/fish/vendor_completions.d/soda.fish

%files
%license LICENSE
%doc README.md CHANGELOG.md
%{_bindir}/soda
%{_datadir}/bash-completion/completions/soda
%{_datadir}/zsh/site-functions/_soda
%{_datadir}/fish/vendor_completions.d/soda.fish

%changelog

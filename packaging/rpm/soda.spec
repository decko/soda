%global soda_version %{?soda_version}%{!?soda_version:0.0.0}
%global goipath         github.com/decko/soda
%global gomodcache      %{_builddir}/gomodcache

Name:           soda
Version:        %{soda_version}
Release:        1%{?dist}
Summary:        AI-powered software development pipeline orchestrator with sandbox support

License:        MIT
URL:            https://%{goipath}
Source0:        soda-%{soda_version}.tar.gz

ExclusiveArch:  x86_64

BuildRequires:  golang >= 1.25
BuildRequires:  gcc
BuildRequires:  git-lfs
BuildRequires:  python3
BuildRequires:  curl

Conflicts:      soda-minimal

%description
Soda is an AI-powered software development pipeline orchestrator that drives
Claude Code through multi-phase workflows. This build includes CGO support
and kernel-enforced process isolation via Landlock, seccomp, and cgroups
(provided by the go-arapuca library).

%prep
%setup -q -n soda-%{soda_version}

%build
export GOMODCACHE=%{gomodcache}
export GOFLAGS="-mod=mod"
export CGO_ENABLED=1

# Resolve module dependencies
go mod download

# Fetch go-arapuca LFS binary
arapuca_version=$(go list -m -f '{{.Version}}' github.com/sergio-correia/go-arapuca)
mod_dir="${GOMODCACHE}/github.com/sergio-correia/go-arapuca@${arapuca_version}"
lib_path="${mod_dir}/lib/linux_amd64/libarapuca.a"

if [ -f "$lib_path" ] && file "$lib_path" | grep -q "ASCII"; then
    oid=$(awk '/^oid sha256:/{print substr($2,8)}' "$lib_path")
    url="https://github.com/sergio-correia/go-arapuca.git/info/lfs/objects/batch"
    response=$(curl -s -X POST "$url" \
        -H "Content-Type: application/vnd.git-lfs+json" \
        -d "{\"operation\":\"download\",\"objects\":[{\"oid\":\"${oid}\",\"size\":1}]}")
    download_url=$(echo "$response" | python3 -c \
        "import sys,json; d=json.load(sys.stdin); print(d['objects'][0]['actions']['download']['href'])" 2>/dev/null) || {
        echo "ERROR: Failed to resolve LFS object for libarapuca.a"
        exit 1
    }
    chmod -R u+w "$(dirname "$lib_path")"
    curl -sL "$download_url" -o "$lib_path"
    echo "Fetched libarapuca.a ($(wc -c < "$lib_path") bytes)"
fi

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

#!/bin/bash
# Build SRPMs for soda and soda-minimal from the current git HEAD.
# Usage: ./packaging/rpm/build-srpm.sh [version]
#
# Prerequisites: sudo dnf install rpm-build rpmdevtools go-rpm-macros
#
# The version defaults to the latest git tag without the 'v' prefix.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Determine version
if [ $# -ge 1 ]; then
    VERSION="$1"
else
    TAG="$(git -C "$REPO_ROOT" describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")"
    VERSION="${TAG#v}"
fi

echo "Building SRPMs for soda ${VERSION}"

# Setup rpmbuild tree
RPMBUILD_DIR="${REPO_ROOT}/rpmbuild"
mkdir -p "${RPMBUILD_DIR}"/{SOURCES,SPECS,SRPMS}

# Create source tarball from git archive
TARBALL="${RPMBUILD_DIR}/SOURCES/soda-${VERSION}.tar.gz"
echo "Creating source tarball..."
git -C "$REPO_ROOT" archive --format=tar.gz --prefix="soda-${VERSION}/" HEAD > "$TARBALL"
echo "  → ${TARBALL} ($(du -h "$TARBALL" | cut -f1))"

# Create vendor tarball
VENDOR_TARBALL="${RPMBUILD_DIR}/SOURCES/soda-${VERSION}-vendor.tar.bz2"
echo "Creating vendor tarball..."
TMPDIR=$(mktemp -d)
trap "rm -rf $TMPDIR" EXIT

# Extract source and vendor deps
tar -xzf "$TARBALL" -C "$TMPDIR"
(cd "$TMPDIR/soda-${VERSION}" && go mod vendor)
# Create vendor tarball relative to source root
(cd "$TMPDIR/soda-${VERSION}" && tar -cjf "$VENDOR_TARBALL" vendor/)
echo "  → ${VENDOR_TARBALL} ($(du -h "$VENDOR_TARBALL" | cut -f1))"

# Copy spec files and bake in the version so COPR rebuilds use the right value.
for spec in soda.spec soda-minimal.spec; do
    sed "s/^Version:.*$/Version:        ${VERSION}/" \
        "${SCRIPT_DIR}/${spec}" > "${RPMBUILD_DIR}/SPECS/${spec}"
done

# Build SRPMs
echo ""
echo "Building soda SRPM (full, CGO)..."
rpmbuild -bs \
    --define "_topdir ${RPMBUILD_DIR}" \
    "${RPMBUILD_DIR}/SPECS/soda.spec"

echo ""
echo "Building soda-minimal SRPM (static, no CGO)..."
rpmbuild -bs \
    --define "_topdir ${RPMBUILD_DIR}" \
    "${RPMBUILD_DIR}/SPECS/soda-minimal.spec"

echo ""
echo "SRPMs:"
ls -1 "${RPMBUILD_DIR}/SRPMS/"*.src.rpm

echo ""
echo "Next steps:"
echo "  1. Submit minimal: copr-cli build decko/soda ${RPMBUILD_DIR}/SRPMS/soda-minimal-${VERSION}-1.*.src.rpm"
echo "  2. Submit full:    copr-cli build decko/soda ${RPMBUILD_DIR}/SRPMS/soda-${VERSION}-1.*.src.rpm"
echo "  3. Test locally:   mock -r fedora-rawhide-x86_64 ${RPMBUILD_DIR}/SRPMS/soda-minimal-${VERSION}-1.*.src.rpm"

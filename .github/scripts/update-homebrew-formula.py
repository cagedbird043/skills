#!/usr/bin/env python3
"""Update the homebrew formula version and sha256 checksums."""
import re
import sys

formula_path = sys.argv[1]
version = sys.argv[2]
sha_linux = sys.argv[3]
sha_darwin_arm = sys.argv[4]
sha_completion = sys.argv[5] if len(sys.argv) > 5 else ""

with open(formula_path) as f:
    content = f.read()

# Update version
content = re.sub(r'version ".*"', f'version "{version}"', content)

# Update sha256 for binary platforms (sha256 line follows url line)
platforms = [
    ('skills-darwin-arm64', sha_darwin_arm),
    ('skills-linux-amd64', sha_linux),
]

for url_suffix, new_sha in platforms:
    pattern = re.escape(f'url "https://github.com/cagedbird043/skills/releases/download/v#{{version}}/{url_suffix}"')
    url_match = re.search(pattern, content)
    if not url_match:
        pattern = re.escape(f'{url_suffix}"')
        url_match = re.search(pattern, content)
    if url_match:
        after_url = content[url_match.end():]
        sha_line_match = re.search(r'sha256 "[^"]*"', after_url)
        if sha_line_match:
            old_sha = sha_line_match.group()
            new = f'sha256 "{new_sha}"'
            content = content.replace(old_sha, new, 1)

# Update completion resource URL + SHA
resource_url_pattern = r'(releases/download/)v[^/]+(_skills")'
content = re.sub(resource_url_pattern, rf'\g<1>v{version}\g<2>', content)
if sha_completion:
    resource_match = re.search(r'resource "completion"', content)
    if resource_match:
        after_resource = content[resource_match.end():]
        sha_line_match = re.search(r'sha256 "[^"]*"', after_resource)
        if sha_line_match:
            old_sha = sha_line_match.group()
            content = content.replace(old_sha, f'sha256 "{sha_completion}"', 1)

with open(formula_path, 'w') as f:
    f.write(content)

print(f"Updated {formula_path} to version {version}")

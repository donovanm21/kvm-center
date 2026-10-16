"""Tag releases must use the same fresh-SPA packaging entrypoint as acceptance."""
from pathlib import Path
import unittest
ROOT = Path(__file__).resolve().parents[2]
class ReleaseWorkflowTests(unittest.TestCase):
    def test_release_uses_shared_fresh_package_and_exact_identity(self):
        workflow = (ROOT / '.github/workflows/release.yml').read_text()
        self.assertIn('actions/setup-node@v7', workflow)
        self.assertIn('VERSION: ${{ github.ref_name }}', workflow)
        self.assertIn('GIT_SHA: ${{ github.sha }}', workflow)
        self.assertIn('test ! -e dist', workflow)
        self.assertIn('bash scripts/build-package.sh dist', workflow)
        self.assertIn('sha256sum --check SHA256SUMS', workflow)
        self.assertNotIn('go mod tidy', workflow)
        self.assertNotIn('go build', workflow)
        package = (ROOT / 'scripts/build-package.sh').read_text()
        self.assertLess(package.index('scripts/build-webui.sh'), package.index('scripts/build-release.sh'))
if __name__ == '__main__': unittest.main()

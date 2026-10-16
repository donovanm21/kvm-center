"""Offline checks for the approved disposable integration workflow contract."""
from pathlib import Path
import re
import subprocess
import unittest

WORKFLOW = Path(__file__).resolve().parents[2] / '.github/workflows/test.yml'


def job_block(name):
    text = WORKFLOW.read_text()
    return re.search(
        rf'^  {re.escape(name)}:\n(.*?)(?=^  [a-z][a-z-]*:|\Z)', text, re.M | re.S).group(1)


class IntegrationWorkflowTests(unittest.TestCase):
    def test_disposable_jobs_are_bounded_and_local(self):
        for name, limit in [('mongo-integration', 15), ('browser-integration', 20)]:
            with self.subTest(job=name):
                block = job_block(name)
                self.assertIn('runs-on: ubuntu-24.04', block)
                self.assertIn(f'timeout-minutes: {limit}', block)
                self.assertIn('permissions:\n      contents: read', block)
                self.assertIn('cancel-in-progress: true', block)
                self.assertIn('image: mongo:7.0.45-jammy', block)
                self.assertIn("'127.0.0.1:27017:27017'", block)
                self.assertIn('KVMC_TEST_MONGO_URI: mongodb://127.0.0.1:27017', block)
                self.assertIn('ref: ${{ github.event.pull_request.head.sha || github.sha }}', block)
                self.assertIn('persist-credentials: false', block)
                self.assertNotIn('secrets.', block)
                self.assertNotIn('volumes:', block)
                self.assertNotIn('docker/login-action', block)

    def test_real_profiles_and_browser_engines_are_required(self):
        mongo = job_block('mongo-integration')
        browser = job_block('browser-integration')
        self.assertIn('python3 -m unittest discover -s scripts/tests -v', mongo)
        self.assertIn('python3 scripts/validate.py --profile mongo --timeout 600', mongo)
        self.assertIn('npx --no-install playwright install --with-deps chromium webkit', browser)
        self.assertIn('KVMC_TEST_BACKEND: mongo', browser)
        self.assertIn('run: scripts/run-browser-fixture.sh', browser)
        self.assertNotIn('continue-on-error', mongo + browser)

    def test_cleanup_and_artifacts_are_always_checked_and_allowlisted(self):
        for name, expected in [
                ('mongo-integration', ['.validation/**/report.json', '.validation/**/mongo.log']),
                ('browser-integration', ['.validation/**/report.json', '.validation/**/browser.log',
                                         '.validation/**/browser-results.json'])]:
            with self.subTest(job=name):
                block = job_block(name)
                self.assertIn('name: Verify fixture database cleanup\n        if: always()', block)
                self.assertIn('d.name.startsWith("kvmc_validation_")', block)
                self.assertIn('if (left.length)', block)
                self.assertIn('quit(1)', block)
                self.assertIn('name: Preserve validation evidence\n        if: always()', block)
                self.assertIn('include-hidden-files: true', block)
                self.assertIn('if-no-files-found: error', block)
                self.assertIn('retention-days: 7', block)
                paths = re.findall(r'^            (\.validation/.*)$', block, re.M)
                self.assertEqual(paths, expected)

    def test_summary_requires_every_job_success(self):
        block = job_block('summary')
        names = ['lint-test', 'webui-audit', 'gitleaks', 'mongo-integration', 'browser-integration']
        self.assertIn('needs: [' + ', '.join(names) + ']', block)
        self.assertIn('if: always()', block)
        script = block.split('        run: |\n', 1)[1]
        script = '\n'.join(line[10:] if line.startswith('          ') else line
                           for line in script.splitlines())
        for failed in [None] + names:
            for outcome in (['success'] if failed is None else ['failure', 'cancelled', 'skipped']):
                rendered = script
                for name in names:
                    rendered = rendered.replace('${{ needs.' + name + '.result }}',
                                                outcome if name == failed else 'success')
                result = subprocess.run(['bash', '-c', rendered], capture_output=True, text=True)
                with self.subTest(failed=failed, outcome=outcome):
                    self.assertEqual(result.returncode, 0 if failed is None else 1)


if __name__ == '__main__':
    unittest.main()

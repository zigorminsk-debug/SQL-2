import unittest
import analysis


class RecommendationsTests(unittest.TestCase):
    def test_no_forced_rcsi_or_universal_parallelism(self):
        data = {'configs': [{'name': 'max degree of parallelism', 'value_in_use': 0}],
                'sys_info': {'cpu_count': 16}}
        findings = analysis.build_findings(data, blocking_events=[{'session_id': 2, 'blocking_session_id': 1}])
        sql = '\n'.join(f.get('sql') or '' for f in findings)
        self.assertNotIn('ROLLBACK IMMEDIATE', sql)
        self.assertNotIn("parallelism', 8", sql)
        self.assertTrue(any('REPEATABLE READ' in f['action'] for f in findings))

    def test_memory_advice_has_no_blind_percentage_sql(self):
        data = {'configs': [{'name':'max server memory (MB)', 'value_in_use':2147483647}],
                'sys_info': {'phys_kb': 32 * 1048576}}
        findings = analysis.build_findings(data)
        self.assertTrue(any(f['id'] == 'MEM-01' for f in findings))
        self.assertFalse(any(f.get('sql') for f in findings))


if __name__ == '__main__': unittest.main()

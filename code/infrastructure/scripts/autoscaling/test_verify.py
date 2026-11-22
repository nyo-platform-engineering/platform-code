"""A newly created HPA has no usable CPU samples until metrics arrive."""
import unittest
from verify import cpu_utilizations, idle_baseline


def state(status):
    return {'desired': 3, 'ready': 3, 'hpas': [{'status': status}]}


class BaselineTests(unittest.TestCase):
    def test_pending_metrics_keep_waiting(self):
        for status in (None, {}, {'currentMetrics': None}, {'currentMetrics': []}):
            with self.subTest(status=status):
                self.assertEqual(cpu_utilizations(state(status)), [])
                self.assertFalse(idle_baseline(state(status)))

    def test_metrics_arriving_after_null_release_baseline(self):
        self.assertFalse(idle_baseline(state({'currentMetrics': None})))
        self.assertTrue(idle_baseline(state({'currentMetrics': [{
            'containerResource': {'name': 'cpu', 'current': {'averageUtilization': 20}}}]})))

    def test_unknown_or_high_cpu_never_counts_as_idle(self):
        for current in (None, {}, {'averageUtilization': None}, {'averageUtilization': 60}):
            with self.subTest(current=current):
                self.assertFalse(idle_baseline(state({'currentMetrics': [{
                    'containerResource': {'name': 'cpu', 'current': current}}]})))

    def test_not_ready_is_not_idle(self):
        sample = state({'currentMetrics': [{'resource': {
            'name': 'cpu', 'current': {'averageUtilization': 10}}}]})
        self.assertTrue(idle_baseline(sample))
        sample['ready'] = 2
        self.assertFalse(idle_baseline(sample))


if __name__ == '__main__':
    unittest.main()

"""Local controller ownership/ordering checks; no cluster requests."""
import unittest
from unittest.mock import patch
import main as toggle


class ToggleTests(unittest.TestCase):
    def test_reject_unowned_object(self):
        with self.assertRaisesRegex(ValueError, 'not owned'):
            toggle.owned({'metadata': {'labels': {}}})

    @patch.object(toggle, 'kube')
    @patch.object(toggle, 'hpas', return_value=[{'metadata': {'name': 'native-hpa'}}])
    @patch.object(toggle, 'optional', return_value=None)
    @patch.object(toggle, 'guard')
    def test_off_rejects_foreign_hpa_before_scale(self, guard, optional, hpas, kube):
        with self.assertRaisesRegex(ValueError, 'foreign controller'):
            toggle.disable()
        kube.assert_not_called()

    @patch.object(toggle, 'kube')
    @patch.object(toggle, 'hpas', return_value=[])
    @patch.object(toggle, 'optional', return_value=None)
    @patch.object(toggle, 'guard')
    def test_off_without_keda_restores_fixed_count(self, guard, optional, hpas, kube):
        toggle.disable()
        kube.assert_called_once_with('scale', 'deployment/aggregator', '--replicas=3')

    @patch.object(toggle, 'kube')
    @patch.object(toggle, 'hpas', return_value=[])
    @patch.object(toggle, 'optional')
    @patch.object(toggle, 'guard')
    def test_delete_owned_scaler_before_manual_restore(self, guard, optional, hpas, kube):
        optional.side_effect = [{'metadata': {'name': 'scaledobjects.keda.sh'}},
                                {'metadata': {'uid': 'ours', 'labels': {toggle.OWNER: 'autoscaling-task'}},
                                 'spec': {'scaleTargetRef': {'name': 'aggregator'}}}]
        toggle.disable()
        self.assertEqual(kube.call_args_list[0].args, ('delete', 'scaledobject', toggle.NAME, '--wait=true', '--timeout=30s'))
        self.assertEqual(kube.call_args_list[1].args, ('scale', 'deployment/aggregator', '--replicas=3'))

    @patch.object(toggle, 'kube')
    def test_m2_pause_refuses_mutation(self, kube):
        kube.return_value = {'metadata': {'annotations': {'argocd.argoproj.io/skip-reconcile': 'true'}}}
        with self.assertRaisesRegex(ValueError, 'M2 test'):
            toggle.guard()
        self.assertEqual(kube.call_count, 1)


if __name__ == '__main__':
    unittest.main()

import unittest

import nbtlib
from nbt import nbt

from scan import identifiers


class IdentifierTests(unittest.TestCase):
    def test_recorded_plaza_palette_forms(self):
        recorded = {'palette': [
            {'': 'minecraft:white_concrete'},
            {'id': 'minecraft:quartz_stairs', 'properties': {'facing': 'north'}},
            'minecraft:air',
        ]}
        self.assertEqual(identifiers(recorded), {
            'minecraft:white_concrete', 'minecraft:quartz_stairs', 'minecraft:air',
        })

    def test_nbt_compound_preserves_anonymous_palette_name(self):
        root = nbt.TAG_Compound()
        palette = nbt.TAG_List(name='palette', type=nbt.TAG_Compound)
        entry = nbt.TAG_Compound()
        entry.tags.append(nbt.TAG_String(name='', value='minecraft:white_concrete'))
        palette.tags.append(entry)
        root.tags.append(palette)
        self.assertEqual(identifiers(root), {'minecraft:white_concrete'})
        self.assertEqual(identifiers(nbtlib.Compound({'palette': nbtlib.List[nbtlib.Compound]([
            nbtlib.Compound({'': nbtlib.String('minecraft:white_concrete')}),
        ])})), {'minecraft:white_concrete'})


if __name__ == '__main__':
    unittest.main()

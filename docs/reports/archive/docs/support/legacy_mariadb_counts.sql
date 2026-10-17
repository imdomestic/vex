SELECT 'CMI_inventories', COUNT(*) FROM minecraft.CMI_inventories
UNION ALL SELECT 'CMI_playtime', COUNT(*) FROM minecraft.CMI_playtime
UNION ALL SELECT 'CMI_playtimereward', COUNT(*) FROM minecraft.CMI_playtimereward
UNION ALL SELECT 'CMI_users', COUNT(*) FROM minecraft.CMI_users;

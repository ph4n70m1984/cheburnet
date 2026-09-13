'use strict';
'require baseclass';

var ALLOW_DOMAIN_CATEGORIES = [
    { tag: 'anime',          title: 'Anime' },
    { tag: 'block',          title: 'Block' },
    { tag: 'cloudflare',     title: 'Cloudflare' },
    { tag: 'cloudfront',     title: 'CloudFront' },
    { tag: 'digitalocean',   title: 'DigitalOcean' },
    { tag: 'discord',        title: 'Discord' },
    { tag: 'geoblock',       title: 'Geoblock' },
    { tag: 'google_ai',      title: 'Google AI' },
    { tag: 'google_meet',    title: 'Google Meet' },
    { tag: 'google_play',    title: 'Google Play' },
    { tag: 'hdrezka',        title: 'HDRezka' },
    { tag: 'hetzner',        title: 'Hetzner' },
    { tag: 'hodca',          title: 'Hodca' },
    { tag: 'meta',           title: 'Meta' },
    { tag: 'news',           title: 'News' },
    { tag: 'ovh',            title: 'OVH' },
    { tag: 'porn',           title: 'Porn' },
    { tag: 'roblox',         title: 'Roblox' },
    { tag: 'russia_inside',  title: 'Russia Inside / РКН' },
    { tag: 'russia_outside', title: 'Russia Outside' },
    { tag: 'telegram',       title: 'Telegram' },
    { tag: 'tiktok',         title: 'TikTok' },
    { tag: 'twitter',        title: 'Twitter / X' },
    { tag: 'ukraine_inside', title: 'Ukraine Inside' },
    { tag: 'youtube',        title: 'YouTube' }
];

return baseclass.extend({
    categories: ALLOW_DOMAIN_CATEGORIES
});
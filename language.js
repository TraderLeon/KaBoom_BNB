const { t } = require('../config/translations');
const dbClient = require('../config/database');
const { getContextId } = require('../utils');  // Import the helper function
const { Markup } = require("telegraf");

// Language names in their respective languages
const languageNames = {
    en: 'English',
    zh: '中文',
    ja: '日本語',
    ko: '한국어',
    tr: 'Türkçe',
    ru: 'Русский',
    vi: 'Tiếng Việt',
    id: 'Bahasa Indonesia',
    ar: 'العربية',
    fr: 'Français',
    de: 'Deutsch',
    es: 'Español',
    fi: 'Suomi'
};

const languages = Object.keys(languageNames);

async function chooseLanguageCommand(ctx) {
    const langButtons = languages.map((lang) => [{ text: languageNames[lang], callback_data: `lang_${lang}` }]);

    const message = t('en', 'choose_language'); // Default to English for the language selection message
    const languageButtons = {
        reply_markup: {
            inline_keyboard: langButtons
        }
    };

    await ctx.reply(message, languageButtons);
}

async function languageCommand(ctx) {
    const lang = ctx.match[1];
    const contextId = getContextId(ctx);  // Use helper function to get correct ID
    const xAccountButton = "𝕏 Account";

    // Store user's language preference in the database
    await dbClient.query(
        `UPDATE users SET language = $1 WHERE telegram_id = $2`,
        [lang, contextId]  // Use contextId instead of ctx.from.id to handle both group and private chats
    );

    const message = t(lang, 'welcome_message');
    const startButtons = {
        reply_markup: {
            inline_keyboard: [
                [ 
                    Markup.button.url(t(lang, 'subscribe_signal'), "https://t.me/kaboom_signal")
                    ],
                [ 
                    Markup.button.url(t(lang, 'trading_meme'), "https://t.me/kaboom_auth_bot/kaboom")
                    ],
                [{ text: t(lang, 'latest_dex_rank'), callback_data: 'latest_dex_rank' }],
                ///[{ text: t(userLang, 'choose_chain'), callback_data: 'choose_chain' }], 
                ///[{ text: t(userLang, 'cex_stock_technicals'), callback_data: 'cex_stock_technicals' }],
                ///[{ text: t(userLang, 'manage_subscription'), callback_data: 'manage_subscription' }],
                ///[{ text: t(userLang, 'my_market_call'), callback_data: 'my_market_call' }],
                [{ text: t(lang, 'invite_friend'), callback_data: 'invite_friend' }],
                [{ text: t(lang, 'about'), callback_data: 'about' }],
                [{ text: t(lang, 'choose_language'), callback_data: 'choose_language' }],
                [
                    Markup.button.url(xAccountButton, "https://x.com/kaboomalpha"),
                  ],
            ]
        }
    };

    await ctx.reply(message, startButtons);
}

module.exports = { chooseLanguageCommand, languageCommand };

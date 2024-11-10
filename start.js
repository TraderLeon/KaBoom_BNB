const { t } = require('../config/translations');
const dbClient = require('../config/database');
const { getContextId } = require('../utils');  // Import the helper function
const { myMarketCallCommand } = require('./market_call'); // Import the new function
const { Markup } = require("telegraf");

async function startCommand(ctx) {
    const contextId = getContextId(ctx);  // Use getContextId to handle both group and private chats
    console.log(`Start command triggered for context: ${contextId}`);

    // Check if the bot is in a group chat and if the user is an admin
    if (ctx.chat.type === 'group' || ctx.chat.type === 'supergroup') {
        const member = await ctx.telegram.getChatMember(ctx.chat.id, ctx.from.id);
        if (member.status !== 'administrator' && member.status !== 'creator') {
            // Only admins or creators can use the bot in a group chat
            return ctx.reply(t('en', 'admin_only')); // Reply with a message, or just ignore the command
        }
    }

    const res = await dbClient.query(`SELECT language FROM users WHERE telegram_id = $1`, [contextId]);
    const userLang = res.rows.length > 0 ? res.rows[0].language : 'en';
    const xAccountButton = "𝕏 Account";
    const telegramButton = "💸Alpha Group";
    const message = t(userLang, 'welcome_message');
    const startButtons = {
        reply_markup: {
            inline_keyboard: [
                [ 
                    Markup.button.url(t(userLang, 'subscribe_signal'), "https://t.me/kaboom_signal")
                ],
                [ 
                    Markup.button.url(t(userLang, 'trading_meme'), "https://t.me/kaboom_auth_bot/kaboom")
                ],
                [{ text: t(userLang, 'latest_dex_rank'), callback_data: 'latest_dex_rank' }],
                [{ text: t(userLang, 'invite_friend'), callback_data: 'invite_friend' }],
                [{ text: t(userLang, 'about'), callback_data: 'about' }],
                [{ text: t(userLang, 'choose_language'), callback_data: 'choose_language' }],
                [
                    Markup.button.url(xAccountButton, "https://x.com/kaboomalpha"),
                    Markup.button.url(telegramButton, "https://t.me/kaboom_alpha")
                ],
            ]
        }
    };

    await ctx.reply(message, startButtons);
}

module.exports = { startCommand };
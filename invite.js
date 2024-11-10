const { t } = require('../config/translations');
const dbClient = require('../config/database');
const { getContextId } = require('../utils');  // Import the helper function

// Invite Friend command
async function inviteCommand(ctx) {
    const contextId = getContextId(ctx);  // Use getContextId to handle both group and private chats
    console.log(`Invite command triggered for context: ${contextId}`);

    // Retrieve the user's language from the database
    const res = await dbClient.query(`SELECT language FROM users WHERE telegram_id = $1`, [contextId]);
    const userLang = res.rows.length > 0 ? res.rows[0].language : 'en'; // Default to English

    // Define the message to share
    const shareMessage = t(userLang, 'invite_statement');
    const botLink = `https://t.me/KaBoom_Alpha_Bot?start=${contextId}`; // Replace with your bot link

    // Create the share and copy buttons
    const shareButtons = {
        reply_markup: {
            inline_keyboard: [
                [{
                    text: t(userLang, 'share_invite'),
                    url: `https://t.me/share/url?url=${encodeURIComponent(botLink)}&text=${encodeURIComponent(shareMessage)}`
                }],
                [{
                    text: t(userLang, 'copy_link'),
                    callback_data: 'copy_link'
                }],
                [{ text: t(userLang, 'back'), callback_data: 'back_to_start' }]
            ]
        }
    };

    await ctx.reply(t(userLang, 'invite_message'), shareButtons);
}

// Command to handle the "Copy Link" action
async function copyLinkCommand(ctx) {
    const contextId = getContextId(ctx);  // Use getContextId to handle both group and private chats
    console.log(`Copy link command triggered for context: ${contextId}`);

    // Retrieve the user's language from the database
    const res = await dbClient.query(`SELECT language FROM users WHERE telegram_id = $1`, [contextId]);
    const userLang = res.rows.length > 0 ? res.rows[0].language : 'en';

    // Define the message and bot link
    const shareMessage = t(userLang, 'invite_statement');
    const botLink = `https://t.me/KaBoom_Alpha_Bot?start=${contextId}`; // Replace with your bot link

    // Send the invite message directly as a reply
    await ctx.reply(`${shareMessage} ${botLink}`);
}

module.exports = { inviteCommand, copyLinkCommand };

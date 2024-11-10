const { t } = require('../config/translations');
const dbClient = require('../config/database');
const { getContextId } = require('../utils');  // Import the helper function

async function aboutCommand(ctx) {
    const contextId = getContextId(ctx);  // Use getContextId to handle both group and private chats
    console.log(`About command triggered for context: ${contextId}`);

    // Check the user's language choice from the database
    const res = await dbClient.query(`SELECT language FROM users WHERE telegram_id = $1`, [contextId]);
    const userLang = res.rows.length > 0 ? res.rows[0].language : 'en'; // Default to English if no language is set

    // Get the translated about message based on the user's language
    const message = t(userLang, 'about_message');

    // Define the Back button
    const backButton = {
        reply_markup: {
            inline_keyboard: [
                [{ text: t(userLang, 'back'), callback_data: 'back_to_start' }]
            ]
        }
    };

    // Send the about message with the Back button
    await ctx.reply(message, backButton);
}

module.exports = { aboutCommand };

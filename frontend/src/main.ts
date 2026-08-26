import { createApp } from 'vue';
import { createPinia } from 'pinia';
import ArcoVue from '@arco-design/web-vue';
import '@arco-design/web-vue/dist/arco.css';
import App from './App.vue';
import router from './router';
import './styles/global.css';

createApp(App).use(createPinia()).use(router).use(ArcoVue).mount('#app');
